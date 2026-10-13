package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

func reqCtx(host string, headers map[string]string) *fasthttp.RequestCtx {
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetHost(host)
	ctx.Request.SetRequestURI("/api/setup/complete")
	for k, v := range headers {
		ctx.Request.Header.Set(k, v)
	}
	return &ctx
}

func TestBrowserOrigin(t *testing.T) {
	cases := []struct {
		name, host   string
		headers      map[string]string
		port, listen int
		origin       string
		https        bool
	}{
		{"same port", "localhost:4747", nil, 4747, 4747, "http://localhost:4747", false},
		{"port change", "192.168.1.5:4747", nil, 9000, 4747, "http://192.168.1.5:9000", false},
		{"ipv6", "[::1]:4747", nil, 9000, 4747, "http://[::1]:9000", false},
		{"proxy https keeps proxy host", "127.0.0.1:4747",
			map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "chat.example.com"}, 9000, 4747,
			"https://chat.example.com", true},
		{"proxy first value", "127.0.0.1:4747",
			map[string]string{"X-Forwarded-Proto": "https, http", "X-Forwarded-Host": "a.example.com:8443, b"}, 4747, 4747,
			"https://a.example.com:8443", true},
		{"default port dropped", "example.com:80", nil, 80, 80, "http://example.com", false},
	}
	for _, c := range cases {
		origin, https := browserOrigin(reqCtx(c.host, c.headers), c.port, c.listen)
		assert.Equal(t, c.origin, origin, c.name)
		assert.Equal(t, c.https, https, c.name)
	}
}

func TestEnvironmentFor(t *testing.T) {
	assert.Equal(t, "production", environmentFor(true))
	assert.Equal(t, "development", environmentFor(false))
}
