package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

const (
	// PollInterval is how often an idle consumer checks the database for jobs
	// enqueued by another process.
	PollInterval = time.Second
	// MaxJobAttempts is how many times a job is tried before it is dropped.
	MaxJobAttempts = 5

	ackTimeout = 5 * time.Second
)

// wakeup lets Enqueue wake every idle consumer in this process.
type wakeup struct {
	mu sync.Mutex
	ch chan struct{}
}

func (w *wakeup) wait() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

func (w *wakeup) notify() {
	w.mu.Lock()
	close(w.ch)
	w.ch = make(chan struct{})
	w.mu.Unlock()
}

// DBQueue is a campaign job queue stored in the campaign_jobs table.
type DBQueue struct {
	db   *gorm.DB
	log  logf.Logger
	wake *wakeup
}

var (
	_ Queue    = (*DBQueue)(nil)
	_ Consumer = (*DBConsumer)(nil)
)

func NewDBQueue(db *gorm.DB, log logf.Logger) *DBQueue {
	return &DBQueue{db: db, log: log, wake: &wakeup{ch: make(chan struct{})}}
}

func (q *DBQueue) EnqueueRecipient(ctx context.Context, job *RecipientJob) error {
	return q.EnqueueRecipients(ctx, []*RecipientJob{job})
}

func (q *DBQueue) EnqueueRecipients(ctx context.Context, jobs []*RecipientJob) error {
	if len(jobs) == 0 {
		return nil
	}
	now := time.Now()
	rows := make([]models.CampaignJob, len(jobs))
	for i, job := range jobs {
		if job.EnqueuedAt.IsZero() {
			job.EnqueuedAt = now
		}
		payload, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("failed to marshal recipient job: %w", err)
		}
		rows[i] = models.CampaignJob{JobType: string(JobTypeRecipient), Payload: string(payload)}
	}
	if err := q.db.WithContext(ctx).CreateInBatches(rows, 500).Error; err != nil {
		return fmt.Errorf("failed to enqueue recipient jobs: %w", err)
	}
	q.wake.notify()
	q.log.Info("Recipient jobs enqueued", "count", len(jobs), "campaign_id", jobs[0].CampaignID)
	return nil
}

func (q *DBQueue) Close() error { return nil }

// NewConsumer returns a consumer that is woken by this queue's Enqueue calls.
func (q *DBQueue) NewConsumer() *DBConsumer {
	host, _ := os.Hostname()
	return &DBConsumer{q: q, id: fmt.Sprintf("worker-%s-%d-%s", host, os.Getpid(), uuid.NewString()[:8])}
}

// DBConsumer claims jobs from campaign_jobs. A job whose handler fails stays
// locked for ClaimMinIdleTime and is then retried, up to MaxJobAttempts.
type DBConsumer struct {
	q  *DBQueue
	id string
}

func (c *DBConsumer) Consume(ctx context.Context, handler JobHandler) error {
	c.q.log.Info("Starting to consume jobs", "consumer_id", c.id)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wake := c.q.wake.wait()
		job, contended, err := c.claim(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.q.log.Error("Failed to claim job", "error", err)
			sleep(ctx, time.Second)
			continue
		}
		if job == nil && contended {
			continue
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
			case <-time.After(PollInterval):
			}
			continue
		}
		c.process(ctx, job, handler)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// claim locks the oldest available job for this consumer, or returns nil.
// contended reports that jobs were available but other consumers claimed them
// first, so more may be waiting.
func (c *DBConsumer) claim(ctx context.Context) (job *models.CampaignJob, contended bool, err error) {
	now := time.Now()
	var cands []models.CampaignJob
	if err := c.q.db.WithContext(ctx).
		Where("locked_until IS NULL OR locked_until < ?", now).
		Order("id").Limit(10).Find(&cands).Error; err != nil {
		return nil, false, err
	}
	for i := range cands {
		res := c.q.db.WithContext(ctx).Model(&models.CampaignJob{}).
			Where("id = ? AND (locked_until IS NULL OR locked_until < ?)", cands[i].ID, now).
			Updates(map[string]any{
				"locked_by":    c.id,
				"locked_until": now.Add(ClaimMinIdleTime),
				"attempts":     gorm.Expr("attempts + 1"),
			})
		if res.Error != nil {
			return nil, false, res.Error
		}
		if res.RowsAffected == 1 {
			cands[i].Attempts++
			return &cands[i], false, nil
		}
	}
	return nil, len(cands) > 0, nil
}

func (c *DBConsumer) process(ctx context.Context, job *models.CampaignJob, handler JobHandler) {
	if job.Attempts > MaxJobAttempts {
		c.q.log.Error("Dropping campaign job after too many attempts", "job_id", job.ID, "attempts", job.Attempts-1)
		c.ack(ctx, job.ID)
		return
	}
	if JobType(job.JobType) != JobTypeRecipient {
		c.q.log.Error("Dropping job with unknown type", "job_id", job.ID, "type", job.JobType)
		c.ack(ctx, job.ID)
		return
	}
	var rj RecipientJob
	if err := json.Unmarshal([]byte(job.Payload), &rj); err != nil {
		c.q.log.Error("Dropping job with invalid payload", "job_id", job.ID, "error", err)
		c.ack(ctx, job.ID)
		return
	}
	if err := handler.HandleRecipientJob(ctx, &rj); err != nil {
		c.q.log.Error("Failed to process job; will retry", "job_id", job.ID, "attempt", job.Attempts, "error", err)
		return
	}
	c.ack(ctx, job.ID)
}

// ack deletes a finished job. It ignores ctx cancellation: a job handled just
// before shutdown would otherwise be re-run once its lock expires.
func (c *DBConsumer) ack(ctx context.Context, id int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
	defer cancel()
	if err := c.q.db.WithContext(ctx).Delete(&models.CampaignJob{}, id).Error; err != nil {
		c.q.log.Error("Failed to delete finished job", "job_id", id, "error", err)
	}
}

func (c *DBConsumer) Close() error { return nil }
