package handlers

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/models"
)

const chatDelayZSetKey = "whatomate:chatbot:delays"

// ChatDelayProcessor resumes chatbot sessions whose Delay nodes have elapsed.
type ChatDelayProcessor struct {
	app      *App
	interval time.Duration
	stopCh   chan struct{}
}

// NewChatDelayProcessor creates a delay resume poller (default ~1s tick).
func NewChatDelayProcessor(app *App, interval time.Duration) *ChatDelayProcessor {
	if interval <= 0 {
		interval = time.Second
	}
	return &ChatDelayProcessor{app: app, interval: interval, stopCh: make(chan struct{})}
}

// Start begins the delay processing loop.
func (p *ChatDelayProcessor) Start(ctx context.Context) {
	p.app.Log.Info("Chat delay processor started", "interval", p.interval)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.app.Log.Info("Chat delay processor stopped by context")
			return
		case <-p.stopCh:
			p.app.Log.Info("Chat delay processor stopped")
			return
		case <-ticker.C:
			p.processDueDelays(ctx)
		}
	}
}

// Stop stops the delay processor.
func (p *ChatDelayProcessor) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

func (p *ChatDelayProcessor) processDueDelays(ctx context.Context) {
	if p.app.Redis == nil {
		return
	}
	now := float64(time.Now().Unix())
	ids, err := p.app.Redis.ZRangeByScore(ctx, chatDelayZSetKey, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatFloat(now, 'f', 0, 64), Count: 50,
	}).Result()
	if err != nil {
		p.app.Log.Warn("chat delay poll failed", "error", err)
		return
	}
	for _, idStr := range ids {
		// Remove first so concurrent pollers don't double-resume.
		removed, err := p.app.Redis.ZRem(ctx, chatDelayZSetKey, idStr).Result()
		if err != nil || removed == 0 {
			continue
		}
		sessionID, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if err := p.app.resumeChatSessionAfterDelay(sessionID); err != nil {
			p.app.Log.Warn("chat delay resume failed", "session", sessionID, "error", err)
		}
	}
}

// scheduleChatDelay enqueues a session for resume after seconds.
func (a *App) scheduleChatDelay(sessionID uuid.UUID, seconds int) {
	if a.Redis == nil || seconds <= 0 {
		return
	}
	resumeAt := time.Now().Add(time.Duration(seconds) * time.Second).Unix()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Redis.ZAdd(ctx, chatDelayZSetKey, redis.Z{
		Score: float64(resumeAt), Member: sessionID.String(),
	}).Err(); err != nil {
		a.Log.Error("failed to schedule chat delay", "session", sessionID, "error", err)
	}
}

// resumeChatSessionAfterDelay continues a graph parked on a delay node.
func (a *App) resumeChatSessionAfterDelay(sessionID uuid.UUID) error {
	var session models.ChatbotSession
	if err := a.DB.First(&session, "id = ?", sessionID).Error; err != nil {
		return err
	}
	if session.Status != models.SessionStatusActive {
		return nil
	}
	if session.CurrentFlowID == nil {
		return nil
	}
	var flow models.ChatbotFlow
	if err := a.DB.First(&flow, "id = ?", *session.CurrentFlowID).Error; err != nil {
		return err
	}
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", session.OrganizationID, session.WhatsAppAccount).First(&account).Error; err != nil {
		return err
	}
	var contact models.Contact
	if err := a.DB.First(&contact, "id = ?", session.ContactID).Error; err != nil {
		return err
	}
	return a.runChatGraph(&account, &contact, &session, &flow, "", "", nil)
}
