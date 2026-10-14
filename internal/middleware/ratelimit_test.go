package middleware_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/middleware"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	mem := store.NewMemory()
	t.Cleanup(mem.Close)

	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Counter:   mem,
		Log:       testutil.NopLogger(),
		Max:       5,
		Window:    10 * time.Second,
		KeyPrefix: "test_allow_" + uuid.New().String()[:8],
	})

	for i := 0; i < 5; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.1:12345"))
		result := rl(req)
		require.NotNil(t, result, "request %d should be allowed", i+1)
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	t.Parallel()
	mem := store.NewMemory()
	t.Cleanup(mem.Close)

	prefix := "test_block_" + uuid.New().String()[:8]
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Counter:   mem,
		Log:       testutil.NopLogger(),
		Max:       3,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	for i := 0; i < 3; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.2:12345"))
		result := rl(req)
		require.NotNil(t, result)
	}

	// 4th request should be blocked
	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.2:12345"))
	result := rl(req)
	assert.Nil(t, result, "request over limit should be blocked")
	assert.Equal(t, fasthttp.StatusTooManyRequests, req.RequestCtx.Response.StatusCode())
	assert.NotEmpty(t, string(req.RequestCtx.Response.Header.Peek("Retry-After")))
}

func TestRateLimit_DifferentIPsGetSeparateLimits(t *testing.T) {
	t.Parallel()
	mem := store.NewMemory()
	t.Cleanup(mem.Close)

	prefix := "test_sep_" + uuid.New().String()[:8]
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Counter:   mem,
		Log:       testutil.NopLogger(),
		Max:       2,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	// Exhaust limit for IP A
	for i := 0; i < 2; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.3:12345"))
		require.NotNil(t, rl(req))
	}

	// IP B should still be allowed
	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.4:12345"))
	result := rl(req)
	assert.NotNil(t, result, "different IP should have its own limit")
}

func TestUserAwareRateLimit_KeysByUserID(t *testing.T) {
	t.Parallel()
	mem := store.NewMemory()
	t.Cleanup(mem.Close)

	prefix := "test_user_" + uuid.New().String()[:8]
	rl := middleware.UserAwareRateLimit(middleware.RateLimitOpts{
		Counter:   mem,
		Log:       testutil.NopLogger(),
		Max:       2,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	userA := uuid.New()
	userB := uuid.New()

	// Exhaust limit for user A (same IP for both users — simulates VPN)
	for i := 0; i < 2; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.5:12345"))
		req.RequestCtx.SetUserValue(middleware.ContextKeyUserID, userA)
		require.NotNil(t, rl(req), "user A request %d should be allowed", i+1)
	}

	// User A is now blocked
	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.5:12345"))
	req.RequestCtx.SetUserValue(middleware.ContextKeyUserID, userA)
	assert.Nil(t, rl(req), "user A should be blocked after exceeding limit")

	// User B on the SAME IP should still be allowed
	req = newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.5:12345"))
	req.RequestCtx.SetUserValue(middleware.ContextKeyUserID, userB)
	assert.NotNil(t, rl(req), "user B on same IP should have its own limit")
}

func TestUserAwareRateLimit_FallsBackToIP_WhenUnauthenticated(t *testing.T) {
	t.Parallel()
	mem := store.NewMemory()
	t.Cleanup(mem.Close)

	prefix := "test_noauth_" + uuid.New().String()[:8]
	rl := middleware.UserAwareRateLimit(middleware.RateLimitOpts{
		Counter:   mem,
		Log:       testutil.NopLogger(),
		Max:       2,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	// No user ID set — should key by IP
	for i := 0; i < 2; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.6:12345"))
		require.NotNil(t, rl(req))
	}

	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.6:12345"))
	assert.Nil(t, rl(req), "unauthenticated requests should be rate limited by IP")
}

func TestRateLimit_FailsOpenOnCounterError(t *testing.T) {
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Counter: failingCounter{}, Log: testutil.NopLogger(), Max: 1, Window: time.Minute, KeyPrefix: "x",
	})
	req := testutil.NewGETRequest(t)
	assert.NotNil(t, rl(req))
	assert.NotNil(t, rl(req))
}

func TestRateLimit_RetryAfterRoundsResetUp(t *testing.T) {
	for _, tc := range []struct {
		resetIn time.Duration
		want    string
	}{
		{1500 * time.Millisecond, "2"},
		{30 * time.Second, "30"},
		{0, "1"},
	} {
		c := &fixedCounter{count: 2, resetIn: tc.resetIn}
		rl := middleware.RateLimit(middleware.RateLimitOpts{
			Counter: c, Log: testutil.NopLogger(), Max: 1, Window: time.Minute, KeyPrefix: "login",
		})
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.7:12345"))
		assert.Nil(t, rl(req))
		assert.Equal(t, tc.want, string(req.RequestCtx.Response.Header.Peek("Retry-After")))
		assert.Equal(t, "ratelimit:login:10.0.0.7", c.key)
	}
}

type fixedCounter struct {
	count   int64
	resetIn time.Duration
	key     string
}

func (c *fixedCounter) Incr(_ context.Context, key string, _ time.Duration) (int64, time.Duration, error) {
	c.key = key
	return c.count, c.resetIn, nil
}

type failingCounter struct{}

func (failingCounter) Incr(context.Context, string, time.Duration) (int64, time.Duration, error) {
	return 0, 0, errors.New("down")
}

// mockAddr implements net.Addr for testing.
type mockAddr string

func (a mockAddr) Network() string { return "tcp" }
func (a mockAddr) String() string  { return string(a) }

// Ensure newTestRequest is available (defined in middleware_test.go already,
// but if running in isolation this provides it).
func init() {
	_ = func() *fastglue.Request {
		ctx := &fasthttp.RequestCtx{}
		return &fastglue.Request{RequestCtx: ctx}
	}
}
