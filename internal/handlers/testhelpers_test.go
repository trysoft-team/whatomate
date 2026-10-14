package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/queue"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/trysoft/whatomate/test/testutil"
)

// appOption configures an App for testing.
type appOption func(*handlers.App)

// withQueue sets the queue on the test App.
func withQueue(q queue.Queue) appOption {
	return func(a *handlers.App) {
		a.Queue = q
	}
}

// withWhatsApp sets the WhatsApp client on the test App.
func withWhatsApp(wa *whatsapp.Client) appOption {
	return func(a *handlers.App) {
		a.WhatsApp = wa
	}
}

// withHTTPClient sets the HTTP client on the test App.
func withHTTPClient(client *http.Client) appOption {
	return func(a *handlers.App) {
		a.HTTPClient = client
	}
}

// newTestApp creates an App instance for testing with a test database, built-in stores and default config.
func newTestApp(t *testing.T, opts ...appOption) *handlers.App {
	t.Helper()

	db := testutil.SetupTestDB(t)
	mem := store.NewMemory()
	t.Cleanup(mem.Close)
	state := store.NewSQLKV(db)
	t.Cleanup(state.Close)
	log := testutil.NopLogger()

	cfg := &config.Config{
		App: config.AppConfig{
			EncryptionKey: "test-encryption-key-for-handlers-longer-than-32-chars",
		},
		JWT: config.JWTConfig{
			Secret:            testutil.TestJWTSecret,
			AccessExpiryMins:  15,
			RefreshExpiryDays: 7,
		},
	}

	app := &handlers.App{
		Config:    cfg,
		DB:        db,
		Log:       log,
		Cache:     mem,
		State:     state,
		Scheduler: store.NewSQLScheduler(db),
		Broker:    store.NewMemoryBroker(),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(app)
	}

	return app
}
