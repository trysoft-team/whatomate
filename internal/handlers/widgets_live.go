package handlers

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/websocket"
)

type widgetLiveDebouncer struct {
	mu      sync.Mutex
	pending map[uuid.UUID]map[string]struct{}
	timers  map[uuid.UUID]*time.Timer
}

var widgetLive = &widgetLiveDebouncer{
	pending: make(map[uuid.UUID]map[string]struct{}),
	timers:  make(map[uuid.UUID]*time.Timer),
}

const widgetLiveDebounce = 2 * time.Second

// NotifyWidgetLiveUpdate queues a source-scoped dashboard refresh for an org.
func (a *App) NotifyWidgetLiveUpdate(orgID uuid.UUID, sources ...string) {
	if a == nil || a.WSHub == nil || orgID == uuid.Nil || len(sources) == 0 {
		return
	}
	widgetLive.mu.Lock()
	defer widgetLive.mu.Unlock()

	set, ok := widgetLive.pending[orgID]
	if !ok {
		set = make(map[string]struct{})
		widgetLive.pending[orgID] = set
	}
	for _, s := range sources {
		if s != "" {
			set[s] = struct{}{}
		}
	}

	if t, ok := widgetLive.timers[orgID]; ok {
		t.Stop()
	}
	widgetLive.timers[orgID] = time.AfterFunc(widgetLiveDebounce, func() {
		widgetLive.flush(a, orgID)
	})
}

func (d *widgetLiveDebouncer) flush(a *App, orgID uuid.UUID) {
	d.mu.Lock()
	set := d.pending[orgID]
	delete(d.pending, orgID)
	delete(d.timers, orgID)
	d.mu.Unlock()

	if len(set) == 0 || a.WSHub == nil {
		return
	}
	sources := make([]string, 0, len(set))
	for s := range set {
		sources = append(sources, s)
	}
	a.WSHub.BroadcastToOrg(orgID, websocket.WSMessage{
		Type: websocket.TypeWidgetLiveUpdate,
		Payload: map[string]any{
			"sources": sources,
		},
	})
}
