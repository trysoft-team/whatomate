package queue

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/zerodha/logf"
)

const (
	// CampaignStatsChannel is the pub/sub channel for campaign stats updates
	CampaignStatsChannel = "whatomate:campaign_stats"
)

// CampaignStatsUpdate represents a campaign stats update message
type CampaignStatsUpdate struct {
	CampaignID     string                `json:"campaign_id"`
	OrganizationID uuid.UUID             `json:"organization_id"`
	Status         models.CampaignStatus `json:"status"`
	SentCount      int                   `json:"sent_count"`
	DeliveredCount int                   `json:"delivered_count"`
	ReadCount      int                   `json:"read_count"`
	FailedCount    int                   `json:"failed_count"`
}

// Publisher publishes campaign stats updates.
type Publisher struct {
	broker store.Broker
	log    logf.Logger
}

func NewPublisher(b store.Broker, log logf.Logger) *Publisher {
	return &Publisher{broker: b, log: log}
}

// PublishCampaignStats publishes a campaign stats update
func (p *Publisher) PublishCampaignStats(ctx context.Context, update *CampaignStatsUpdate) error {
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	if err := p.broker.Publish(ctx, CampaignStatsChannel, payload); err != nil {
		p.log.Error("Failed to publish campaign stats", "error", err, "campaign_id", update.CampaignID)
		return err
	}
	p.log.Debug("Published campaign stats update", "campaign_id", update.CampaignID, "status", update.Status)
	return nil
}

// Subscriber receives campaign stats updates.
type Subscriber struct {
	broker store.Broker
	log    logf.Logger
	stop   func()
}

func NewSubscriber(b store.Broker, log logf.Logger) *Subscriber {
	return &Subscriber{broker: b, log: log}
}

// SubscribeCampaignStats calls handler for each update until ctx is done or Close is called.
// handler may run concurrently with itself and must not block.
func (s *Subscriber) SubscribeCampaignStats(ctx context.Context, handler func(update *CampaignStatsUpdate)) error {
	stop, err := s.broker.Subscribe(ctx, CampaignStatsChannel, func(payload []byte) {
		var update CampaignStatsUpdate
		if err := json.Unmarshal(payload, &update); err != nil {
			s.log.Error("Failed to unmarshal campaign stats update", "error", err)
			return
		}
		handler(&update)
	})
	if err != nil {
		return err
	}
	s.stop = stop
	s.log.Info("Subscribed to campaign stats channel")
	return nil
}

// Close stops the subscription.
func (s *Subscriber) Close() error {
	if s.stop != nil {
		s.stop()
	}
	return nil
}
