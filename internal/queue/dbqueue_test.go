package queue_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/queue"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

type funcHandler func(ctx context.Context, job *queue.RecipientJob) error

func (f funcHandler) HandleRecipientJob(ctx context.Context, job *queue.RecipientJob) error {
	return f(ctx, job)
}

func dbQueue(t *testing.T) (*queue.DBQueue, *gorm.DB) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.Exec("DELETE FROM campaign_jobs").Error)
	return queue.NewDBQueue(db, testutil.NopLogger()), db
}

func jobs(n int) []*queue.RecipientJob {
	out := make([]*queue.RecipientJob, n)
	for i := range out {
		out[i] = &queue.RecipientJob{CampaignID: uuid.New(), RecipientID: uuid.New(), PhoneNumber: "1"}
	}
	return out
}

func TestDBQueue_EachJobHandledOnceAcrossConsumers(t *testing.T) {
	q, db := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	h := funcHandler(func(_ context.Context, j *queue.RecipientJob) error {
		mu.Lock()
		seen[j.RecipientID]++
		mu.Unlock()
		return nil
	})
	for range 3 {
		go func() { _ = q.NewConsumer().Consume(ctx, h) }()
	}

	in := jobs(30)
	require.NoError(t, q.EnqueueRecipients(ctx, in))
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 30
	}, 10*time.Second, "all jobs handled")

	for _, j := range in {
		assert.Equal(t, 1, seen[j.RecipientID])
	}
	testutil.AssertEventually(t, func() bool {
		var n int64
		db.Model(&models.CampaignJob{}).Count(&n)
		return n == 0
	}, 5*time.Second, "acked jobs deleted")
}

func TestDBQueue_EnqueueWakesConsumerImmediately(t *testing.T) {
	q, _ := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan struct{}, 1)
	go func() {
		_ = q.NewConsumer().Consume(ctx, funcHandler(func(context.Context, *queue.RecipientJob) error {
			got <- struct{}{}
			return nil
		}))
	}()
	time.Sleep(200 * time.Millisecond) // consumer is now idle
	start := time.Now()
	require.NoError(t, q.EnqueueRecipient(ctx, jobs(1)[0]))
	select {
	case <-got:
		assert.Less(t, time.Since(start), queue.PollInterval)
	case <-time.After(5 * time.Second):
		t.Fatal("job not handled")
	}
}

func TestDBConsumer_FailedJobRetriedAfterLockExpiry(t *testing.T) {
	q, db := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, q.EnqueueRecipient(ctx, jobs(1)[0]))

	var calls atomic.Int32
	h := funcHandler(func(context.Context, *queue.RecipientJob) error {
		if calls.Add(1) == 1 {
			return errors.New("transient")
		}
		return nil
	})
	go func() { _ = q.NewConsumer().Consume(ctx, h) }()
	testutil.AssertEventually(t, func() bool { return calls.Load() == 1 }, 5*time.Second, "first attempt")

	// Simulate the lock running out instead of waiting ClaimMinIdleTime.
	require.NoError(t, db.Model(&models.CampaignJob{}).Where("1 = 1").
		Update("locked_until", time.Now().Add(-time.Second)).Error)
	testutil.AssertEventually(t, func() bool { return calls.Load() == 2 }, 5*time.Second, "retried")
}

func TestDBConsumer_GivesUpAfterMaxAttempts(t *testing.T) {
	q, db := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, q.EnqueueRecipient(ctx, jobs(1)[0]))
	require.NoError(t, db.Model(&models.CampaignJob{}).Where("1 = 1").
		Update("attempts", queue.MaxJobAttempts).Error)

	var calls atomic.Int32
	go func() {
		_ = q.NewConsumer().Consume(ctx, funcHandler(func(context.Context, *queue.RecipientJob) error {
			calls.Add(1)
			return errors.New("always fails")
		}))
	}()
	testutil.AssertEventually(t, func() bool {
		var n int64
		db.Model(&models.CampaignJob{}).Count(&n)
		return n == 0
	}, 5*time.Second, "job dropped")
	assert.Equal(t, int32(0), calls.Load())
}

func TestDBConsumer_AcksJobFinishedDuringShutdown(t *testing.T) {
	q, db := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, q.EnqueueRecipient(ctx, jobs(1)[0]))

	done := make(chan error, 1)
	go func() {
		done <- q.NewConsumer().Consume(ctx, funcHandler(func(context.Context, *queue.RecipientJob) error {
			cancel()
			return nil
		}))
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not stop")
	}

	var n int64
	require.NoError(t, db.Model(&models.CampaignJob{}).Count(&n).Error)
	assert.Equal(t, int64(0), n, "job sent before shutdown must not be re-run")
}
