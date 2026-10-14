package setup

import (
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"
)

// browserOrigin returns the origin the browser used for ctx and whether it
// was HTTPS: the Origin header when it is a plain http(s) origin, else the
// Host and X-Forwarded-* headers. Without proxy headers, a chosen port other
// than listenPort replaces the origin's port.
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
	if u, err := url.Parse(string(ctx.Request.Header.Peek("Origin"))); err == nil && isOrigin(u) {
		scheme, host = strings.ToLower(u.Scheme), u.Host
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

// isOrigin reports whether u is scheme://host[:port] with an http(s) scheme.
func isOrigin(u *url.URL) bool {
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != "" && u.User == nil &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery
}

// allowedOrigins is the allowed_origins value setup writes. Over HTTPS it is
// the browser origin alone; over plain HTTP it adds every listen URL, so
// teammates on another address of this machine get live updates too.
func allowedOrigins(origin string, https bool, urls []string) string {
	if https {
		return origin
	}
	out := []string{origin}
	for _, u := range urls {
		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return strings.Join(out, ",")
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
