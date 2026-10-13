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
		{"origin header wins over host", "127.0.0.1:4747",
			map[string]string{"Origin": "http://192.168.1.5:4747"}, 4747, 4747, "http://192.168.1.5:4747", false},
		{"origin header port change", "127.0.0.1:4747",
			map[string]string{"Origin": "http://192.168.1.5:4747"}, 9000, 4747, "http://192.168.1.5:9000", false},
		{"origin header behind proxy that rewrites host", "10.0.0.2:4747",
			map[string]string{"Origin": "https://chat.example.com", "X-Forwarded-Proto": "https"}, 9000, 4747,
			"https://chat.example.com", true},
		{"origin header over x-forwarded-host", "127.0.0.1:4747",
			map[string]string{"Origin": "https://chat.example.com", "X-Forwarded-Host": "internal:8080"}, 4747, 4747,
			"https://chat.example.com", true},
		{"null origin ignored", "localhost:4747", map[string]string{"Origin": "null"}, 4747, 4747, "http://localhost:4747", false},
		{"origin with path ignored", "localhost:4747",
			map[string]string{"Origin": "http://elsewhere:4747/x"}, 4747, 4747, "http://localhost:4747", false},
		{"non-http origin ignored", "localhost:4747",
			map[string]string{"Origin": "file://elsewhere"}, 4747, 4747, "http://localhost:4747", false},
	}
	for _, c := range cases {
		origin, https := browserOrigin(reqCtx(c.host, c.headers), c.port, c.listen)
		assert.Equal(t, c.origin, origin, c.name)
		assert.Equal(t, c.https, https, c.name)
	}
}

func TestAllowedOrigins(t *testing.T) {
	urls := []string{"http://localhost:4747", "http://192.168.1.5:4747", "http://10.0.0.7:4747"}
	assert.Equal(t, "http://localhost:4747,http://192.168.1.5:4747,http://10.0.0.7:4747",
		allowedOrigins("http://localhost:4747", false, urls), "development: browser origin plus every listen URL, once each")
	assert.Equal(t, "http://myhost.lan:4747,http://localhost:4747,http://192.168.1.5:4747,http://10.0.0.7:4747",
		allowedOrigins("http://myhost.lan:4747", false, urls), "the browser origin comes first")
	assert.Equal(t, "https://chat.example.com",
		allowedOrigins("https://chat.example.com", true, urls), "production: only the browser origin")
}

func TestEnvironmentFor(t *testing.T) {
	assert.Equal(t, "production", environmentFor(true))
	assert.Equal(t, "development", environmentFor(false))
}
