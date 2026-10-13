package setup

import (
	"net"
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"
)

// browserOrigin returns the origin the browser used for ctx and whether it
// was HTTPS. Without proxy headers, a chosen port other than listenPort
// replaces the origin's port.
func browserOrigin(ctx *fasthttp.RequestCtx, port, listenPort int) (string, bool) {
	scheme := "http"
	if ctx.IsTLS() {
		scheme = "https"
	}
	proxied := false
	if p := firstValue(ctx.Request.Header.Peek("X-Forwarded-Proto")); p != "" {
		scheme, proxied = strings.ToLower(p), true
	}
	host := string(ctx.Request.Header.Host())
	if h := firstValue(ctx.Request.Header.Peek("X-Forwarded-Host")); h != "" {
		host, proxied = h, true
	}
	hostname, hostPort := splitHost(host)
	if !proxied && port != listenPort {
		hostPort = strconv.Itoa(port)
	}
	if (scheme == "http" && hostPort == "80") || (scheme == "https" && hostPort == "443") {
		hostPort = ""
	}
	https := scheme == "https"
	if hostPort != "" {
		return scheme + "://" + net.JoinHostPort(hostname, hostPort), https
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	return scheme + "://" + hostname, https
}

func firstValue(v []byte) string {
	s := string(v)
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func splitHost(h string) (host, port string) {
	if host, port, err := net.SplitHostPort(h); err == nil {
		return host, port
	}
	return strings.Trim(h, "[]"), ""
}

// environmentFor picks the config environment: production forces Secure
// cookies, which browsers drop on plain http://<lan-ip>.
func environmentFor(https bool) string {
	if https {
		return "production"
	}
	return "development"
}
