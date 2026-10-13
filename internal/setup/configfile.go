// Package setup serves the first-run setup wizard when no config file exists.
package setup

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/config"
)

// Values are the settings setup writes to a new config file.
type Values struct {
	Environment    string
	Port           int
	AllowedOrigins string
	Database       config.DatabaseConfig
	JWTSecret      string
	EncryptionKey  string
}

// Render returns the TOML config file for v.
func Render(v Values, now time.Time) []byte {
	var b strings.Builder
	kv := func(k, val string) { b.WriteString(k + " = " + val + "\n") }
	fmt.Fprintf(&b, "# Written by whatomate setup on %s.\n# config.example.toml lists every option.\n", now.Format("2006-01-02"))

	b.WriteString("\n[app]\n")
	kv("environment", quote(v.Environment))
	kv("encryption_key", quote(v.EncryptionKey))

	b.WriteString("\n[server]\n")
	kv("host", quote("0.0.0.0"))
	kv("port", strconv.Itoa(v.Port))
	kv("allowed_origins", quote(v.AllowedOrigins))

	d := v.Database
	b.WriteString("\n[database]\n")
	kv("driver", quote(d.Driver))
	if d.Driver == config.DriverSQLite {
		kv("path", quote(d.Path))
	} else {
		kv("host", quote(d.Host))
		kv("port", strconv.Itoa(d.Port))
		kv("user", quote(d.User))
		kv("password", quote(d.Password))
		kv("name", quote(d.Name))
		if d.SSLMode != "" {
			kv("ssl_mode", quote(d.SSLMode))
		}
	}

	b.WriteString("\n[jwt]\n")
	kv("secret", quote(v.JWTSecret))
	return []byte(b.String())
}

// quote returns s as a TOML basic string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// newSecret returns n random bytes, base64url-encoded without padding.
func newSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// storedDBPath writes a SQLite path relative to base when it lies inside it,
// so the config keeps working if the data folder moves with it.
func storedDBPath(path, base string) string {
	if base == "" {
		return path
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}
