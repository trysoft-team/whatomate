package handlers

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/trysoft/whatomate/internal/models"
)

const chatDelayZSetKey = "whatomate:chatbot:delays"
const agentSessionTimeoutZSetKey = "whatomate:chatbot:agent_session_timeouts"

// ChatDelayProcessor resumes chatbot sessions whose Delay nodes have elapsed
// and expires idle chatbot sessions past Session Timeout.
type ChatDelayProcessor struct {
	app            *App
	interval       time.Duration
	stopCh         chan struct{}
	lastIdleExpire time.Time
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
			p.processDueAgentSessionTimeouts(ctx)
			p.processIdleChatbotSessionTimeouts()
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

// cancelChatDelaysForContact removes pending delay resumes for a contact's
// active chatbot sessions (e.g. after an agent takes over).
func (a *App) cancelChatDelaysForContact(contactID uuid.UUID) {
	if a.Redis == nil {
		return
	}
	var sessionIDs []uuid.UUID
	if err := a.DB.Model(&models.ChatbotSession{}).
		Where("contact_id = ? AND status = ?", contactID, models.SessionStatusActive).
		Pluck("id", &sessionIDs).Error; err != nil || len(sessionIDs) == 0 {
		return
	}
	members := make([]any, len(sessionIDs))
	for i, id := range sessionIDs {
		members[i] = id.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Redis.ZRem(ctx, chatDelayZSetKey, members...).Err(); err != nil {
		a.Log.Warn("failed to cancel chat delays", "contact_id", contactID, "error", err)
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
	if a.hasActiveAgentTransfer(session.OrganizationID, contact.ID) {
		a.Log.Info("Skipping delay resume: active agent transfer", "session", sessionID, "contact_id", contact.ID)
		return nil
	}
	if until, ok := contactBotMutedUntil(&contact); ok && time.Now().Before(until) {
		a.Log.Info("Skipping delay resume: bot muted", "session", sessionID, "contact_id", contact.ID, "muted_until", until)
		return nil
	}
	return a.runChatGraph(&account, &contact, &session, &flow, "", "", nil, &chatRunOpts{SystemResume: true})
}

// scheduleAgentSessionTimeout fires when an agent-reply mute expires so we can
// send the optional timeout message and clear mute metadata.
func (a *App) scheduleAgentSessionTimeout(contactID uuid.UUID, until time.Time) {
	if a.Redis == nil || until.IsZero() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Redis.ZAdd(ctx, agentSessionTimeoutZSetKey, redis.Z{
		Score: float64(until.Unix()), Member: contactID.String(),
	}).Err(); err != nil {
		a.Log.Error("failed to schedule agent session timeout", "contact_id", contactID, "error", err)
	}
}

func (a *App) cancelAgentSessionTimeout(contactID uuid.UUID) {
	if a.Redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Redis.ZRem(ctx, agentSessionTimeoutZSetKey, contactID.String()).Err(); err != nil {
		a.Log.Warn("failed to cancel agent session timeout", "contact_id", contactID, "error", err)
	}
}

func (p *ChatDelayProcessor) processDueAgentSessionTimeouts(ctx context.Context) {
	if p.app.Redis == nil {
		return
	}
	now := float64(time.Now().Unix())
	ids, err := p.app.Redis.ZRangeByScore(ctx, agentSessionTimeoutZSetKey, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatFloat(now, 'f', 0, 64), Count: 50,
	}).Result()
	if err != nil {
		p.app.Log.Warn("agent session timeout poll failed", "error", err)
		return
	}
	for _, idStr := range ids {
		removed, err := p.app.Redis.ZRem(ctx, agentSessionTimeoutZSetKey, idStr).Result()
		if err != nil || removed == 0 {
			continue
		}
		contactID, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if err := p.app.handleAgentSessionTimeoutExpiry(contactID); err != nil {
			p.app.Log.Warn("agent session timeout handling failed", "contact_id", contactID, "error", err)
		}
	}
}

// handleAgentSessionTimeoutExpiry clears the mute and optionally sends the
// configured timeout message (empty message = send nothing).
func (a *App) handleAgentSessionTimeoutExpiry(contactID uuid.UUID) error {
	var contact models.Contact
	if err := a.DB.First(&contact, "id = ?", contactID).Error; err != nil {
		return err
	}
	// Still muted into the future (agent replied again) — ignore this stale job.
	if until, ok := contactBotMutedUntil(&contact); ok && time.Now().Before(until) {
		return nil
	}
	orgID := contact.OrganizationID
	_ = a.clearChatbotAgentSessionMute(&contact, orgID)

	account, err := a.resolveWhatsAppAccount(orgID, contact.WhatsAppAccount)
	if err != nil {
		return nil
	}
	settings, err := a.getChatbotSettingsCached(orgID, account.Name)
	if err != nil || settings == nil {
		return nil
	}
	msg := strings.TrimSpace(settings.AgentAssignment.SessionTimeoutMessage)
	if msg == "" {
		return nil
	}
	_, err = a.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: account,
		Contact: &contact,
		Type:    models.MessageTypeText,
		Content: msg,
	}, ChatbotSendOptions())
	return err
}

// idleChatbotSessionExpireInterval is how often the delay processor sweeps
// for chatbot sessions past Session Timeout (even with no inbound message).
const idleChatbotSessionExpireInterval = 30 * time.Second

func (p *ChatDelayProcessor) processIdleChatbotSessionTimeouts() {
	if !p.lastIdleExpire.IsZero() && time.Since(p.lastIdleExpire) < idleChatbotSessionExpireInterval {
		return
	}
	p.lastIdleExpire = time.Now()
	p.app.expireIdleChatbotSessions(uuid.Nil)
}

// expireIdleChatbotSessions marks active chatbot sessions whose idle time exceeds
// the account Session Timeout as status=timeout. If orgID is uuid.Nil, all orgs
// are scanned (background sweep); otherwise only that organization.
func (a *App) expireIdleChatbotSessions(orgID uuid.UUID) {
	type accountPair struct {
		OrganizationID  uuid.UUID
		WhatsAppAccount string
	}
	q := a.DB.Model(&models.ChatbotSession{}).
		Select("DISTINCT organization_id, whats_app_account").
		Where("status = ?", models.SessionStatusActive)
	if orgID != uuid.Nil {
		q = q.Where("organization_id = ?", orgID)
	}
	var pairs []accountPair
	if err := q.Find(&pairs).Error; err != nil {
		a.Log.Warn("Failed to list accounts for idle chatbot session expiry", "error", err)
		return
	}
	if len(pairs) == 0 {
		return
	}

	now := time.Now()
	for _, pair := range pairs {
		mins := 30
		if settings, err := a.getChatbotSettingsCached(pair.OrganizationID, pair.WhatsAppAccount); err == nil && settings != nil && settings.SessionTimeoutMins > 0 {
			mins = settings.SessionTimeoutMins
		}
		cutoff := now.Add(-time.Duration(mins) * time.Minute)

		var stale []models.ChatbotSession
		if err := a.DB.Where(
			"organization_id = ? AND whats_app_account = ? AND status = ? AND last_activity_at <= ?",
			pair.OrganizationID, pair.WhatsAppAccount, models.SessionStatusActive, cutoff,
		).Limit(500).Find(&stale).Error; err != nil {
			a.Log.Warn("Failed to load idle chatbot sessions",
				"error", err, "org_id", pair.OrganizationID, "account", pair.WhatsAppAccount)
			continue
		}
		if len(stale) == 0 {
			continue
		}

		ids := make([]uuid.UUID, 0, len(stale))
		contactSeen := make(map[uuid.UUID]struct{}, len(stale))
		for _, s := range stale {
			ids = append(ids, s.ID)
			contactSeen[s.ContactID] = struct{}{}
		}
		completedAt := now
		if err := a.DB.Model(&models.ChatbotSession{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"status":          models.SessionStatusTimeout,
				"completed_at":    completedAt,
				"current_step":    "",
				"current_flow_id": nil,
			}).Error; err != nil {
			a.Log.Warn("Failed to expire idle chatbot sessions",
				"error", err, "org_id", pair.OrganizationID, "account", pair.WhatsAppAccount)
			continue
		}
		for contactID := range contactSeen {
			a.ClearContactChatbotTracking(contactID)
		}
		a.Log.Info("Expired idle chatbot sessions",
			"org_id", pair.OrganizationID, "account", pair.WhatsAppAccount, "count", len(ids), "timeout_mins", mins)
	}
}
