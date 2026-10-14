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

	"github.com/trysoft/whatomate/internal/calling/defaultaudio"
	"github.com/trysoft/whatomate/internal/config"
)

// DefaultSTUN is the STUN server new configs use for call media.
const DefaultSTUN = "stun:stun.l.google.com:19302"

// Values are the settings setup writes to a new config file.
type Values struct {
	Port     int
	Database config.DatabaseConfig
	// InsecureCookies writes [cookie] secure = false, so login works over
	// plain HTTP until HTTPS is set up.
	InsecureCookies bool
	JWTSecret       string
	EncryptionKey   string
	UploadsDir      string
	AudioDir        string
}

// Render returns the TOML config file for v.
func Render(v Values, now time.Time) []byte {
	var b strings.Builder
	kv := func(k, val string) { b.WriteString(k + " = " + val + "\n") }
	kvc := func(k, val, comment string) { fmt.Fprintf(&b, "%-30s # %s\n", k+" = "+val, comment) }
	fmt.Fprintf(&b, "# Written by whatomate setup on %s.\n# config.example.toml lists every option.\n", now.Format("2006-01-02"))

	b.WriteString("\n[app]\n")
	kv("environment", quote("production"))
	kv("encryption_key", quote(v.EncryptionKey))

	b.WriteString("\n[server]\n")
	kv("host", quote("0.0.0.0"))
	kv("port", strconv.Itoa(v.Port))
	kvc("allowed_origins", quote("*"), "any origin; list origins to restrict")

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

	if v.InsecureCookies {
		b.WriteString("\n[cookie]\n")
		kvc("secure", "false", "set true once whatomate is served over HTTPS")
	}

	b.WriteString("\n[storage]\n")
	kv("type", quote("local"))
	kv("local_path", quote(v.UploadsDir))

	b.WriteString("\n[calling]\n")
	kvc("max_call_duration", "300", "Max call duration in seconds")
	kv("audio_dir", quote(v.AudioDir))
	kv("hold_music_file", quote(defaultaudio.HoldMusicFile))
	kv("ringback_file", quote(defaultaudio.RingbackFile))
	kvc("transfer_timeout_secs", "120", "How long to wait for an agent to accept a transfer")
	kvc("recording_enabled", "true", "Record calls to S3 when configured, otherwise local_path")
	kvc("udp_port_min", "10000", "WebRTC UDP port range start")
	kvc("udp_port_max", "10100", "WebRTC UDP port range end")

	b.WriteString("\n[[calling.ice_servers]]\n")
	kv("urls", "["+quote(DefaultSTUN)+"]")
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
