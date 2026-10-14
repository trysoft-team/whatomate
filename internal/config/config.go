package config

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SHA-1 is mandated by the coturn TURN REST API (RFC draft)
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config holds all configuration for the application
type Config struct {
	App          AppConfig          `koanf:"app"`
	Server       ServerConfig       `koanf:"server"`
	Database     DatabaseConfig     `koanf:"database"`
	Redis        RedisConfig        `koanf:"redis"`
	JWT          JWTConfig          `koanf:"jwt"`
	WhatsApp     WhatsAppConfig     `koanf:"whatsapp"`
	AI           AIConfig           `koanf:"ai"`
	Storage      StorageConfig      `koanf:"storage"`
	DefaultAdmin DefaultAdminConfig `koanf:"default_admin"`
	RateLimit    RateLimitConfig    `koanf:"rate_limit"`
	Cookie       CookieConfig       `koanf:"cookie"`
	Calling      CallingConfig      `koanf:"calling"`
	TTS          TTSConfig          `koanf:"tts"`
	Google       GoogleConfig       `koanf:"google"`
	Retention    RetentionConfig    `koanf:"retention"`
	Backup       BackupConfig       `koanf:"backup"`
}

// RetentionConfig controls how long log-style tables are kept.
type RetentionConfig struct {
	WebhookDeliveryDays int `koanf:"webhook_delivery_days"` // 0/unset = 90, negative = keep forever
}

// GoogleConfig holds the OAuth client used for "Connect Google account"
// credentials. When empty, an organization's Google SSO client is used instead.
type GoogleConfig struct {
	OAuthClientID     string `koanf:"oauth_client_id"`
	OAuthClientSecret string `koanf:"oauth_client_secret"`
}

type TTSConfig struct {
	PiperBinary   string `koanf:"piper_binary"`   // path to piper executable
	PiperModel    string `koanf:"piper_model"`    // path to .onnx voice model
	OpusencBinary string `koanf:"opusenc_binary"` // path to opusenc (defaults to "opusenc")
}

// defaultTURNCredentialTTLSecs is the lifetime of generated coturn REST
// credentials when an ICE server has a secret but no explicit credential_ttl.
const defaultTURNCredentialTTLSecs = 86400 // 24h

type ICEServerConfig struct {
	URLs       []string `koanf:"urls"`
	Username   string   `koanf:"username"`
	Credential string   `koanf:"credential"`
	// Secret enables coturn's "use-auth-secret" (TURN REST API) mode. When set,
	// short-lived credentials are generated per request instead of using the
	// static Username/Credential above.
	Secret string `koanf:"secret"`
	// CredentialTTL is the lifetime (seconds) of generated credentials. Defaults
	// to defaultTURNCredentialTTLSecs when <= 0. Only used when Secret is set.
	CredentialTTL int `koanf:"credential_ttl"`
}

// ResolveCredentials returns the username and credential to advertise for this
// ICE server. When Secret is set it derives short-lived coturn REST credentials
// (use-auth-secret): the username is "<expiry-unix>" — optionally prefixed onto
// any configured Username as "<expiry-unix>:<username>" — and the credential is
// the base64-encoded HMAC-SHA1 of that username keyed by the secret. When Secret
// is empty, the static Username/Credential are returned unchanged.
func (s ICEServerConfig) ResolveCredentials(now time.Time) (username, credential string) {
	if s.Secret == "" {
		return s.Username, s.Credential
	}

	ttl := s.CredentialTTL
	if ttl <= 0 {
		ttl = defaultTURNCredentialTTLSecs
	}

	expiry := now.Add(time.Duration(ttl) * time.Second).Unix()
	username = strconv.FormatInt(expiry, 10)
	if s.Username != "" {
		username = username + ":" + s.Username
	}

	mac := hmac.New(sha1.New, []byte(s.Secret))
	mac.Write([]byte(username))
	credential = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return username, credential
}

type CallingConfig struct {
	MaxCallDuration     int               `koanf:"max_call_duration"`
	AudioDir            string            `koanf:"audio_dir"`
	HoldMusicFile       string            `koanf:"hold_music_file"`
	TransferTimeoutSecs int               `koanf:"transfer_timeout_secs"`
	PerAgentTimeoutSecs int               `koanf:"per_agent_timeout_secs"`
	RingbackFile        string            `koanf:"ringback_file"`
	UDPPortMin          uint16            `koanf:"udp_port_min"` // WebRTC UDP port range start (default: 10000)
	UDPPortMax          uint16            `koanf:"udp_port_max"` // WebRTC UDP port range end (default: 10100)
	PublicIP            string            `koanf:"public_ip"`    // Public IP for NAT mapping (required on AWS/cloud)
	RelayOnly           bool              `koanf:"relay_only"`   // Force all media through TURN relay (no direct UDP)
	ICEServers          []ICEServerConfig `koanf:"ice_servers"`
	RecordingEnabled    bool              `koanf:"recording_enabled"` // Enable call recording (S3 or local_path)
	// TurnBasedAICall disables built-in Gemini Live, so AI Call nodes use the
	// turn-based speech-to-text → LLM → text-to-speech loop.
	TurnBasedAICall bool `koanf:"turn_based_ai_call"`
}

type AppConfig struct {
	Name          string `koanf:"name"`
	Environment   string `koanf:"environment"` // development, staging, production
	Debug         bool   `koanf:"debug"`
	EncryptionKey string `koanf:"encryption_key"` // AES-256 key for encrypting secrets at rest
}

type ServerConfig struct {
	Host           string `koanf:"host"`
	Port           int    `koanf:"port"`
	ReadTimeout    int    `koanf:"read_timeout"`
	WriteTimeout   int    `koanf:"write_timeout"`
	BasePath       string `koanf:"base_path"`       // Base path for frontend (e.g., "/whatomate" for proxy pass)
	PublicURL      string `koanf:"public_url"`      // Optional absolute origin override; empty uses request Host (webhook location URL)
	AllowedOrigins string `koanf:"allowed_origins"` // Comma-separated list of allowed CORS origins
}

const (
	DriverPostgres  = "postgres"
	DriverMySQL     = "mysql"
	DriverSQLite    = "sqlite"
	DriverSQLServer = "sqlserver"
)

// DefaultMaxOpenConns is the connection pool size when max_open_conns is unset.
const DefaultMaxOpenConns = 25

// DefaultPort is the port whatomate (and its setup wizard) listens on when
// [server] port is unset.
const DefaultPort = 4747

type DatabaseConfig struct {
	Driver          string `koanf:"driver"` // postgres (default), mysql, sqlite, sqlserver
	Path            string `koanf:"path"`   // sqlite database file
	Host            string `koanf:"host"`
	Port            int    `koanf:"port"`
	User            string `koanf:"user"`
	Password        string `koanf:"password"`
	Name            string `koanf:"name"`
	SSLMode         string `koanf:"ssl_mode"`
	MaxOpenConns    int    `koanf:"max_open_conns"`
	MaxIdleConns    int    `koanf:"max_idle_conns"`
	ConnMaxLifetime int    `koanf:"conn_max_lifetime"`
}

type RedisConfig struct {
	Host     string `koanf:"host"`
	Port     int    `koanf:"port"`
	Username string `koanf:"username"`
	Password string `koanf:"password"`
	DB       int    `koanf:"db"`
	TLS      bool   `koanf:"tls"`
}

type JWTConfig struct {
	Secret            string `koanf:"secret"`
	AccessExpiryMins  int    `koanf:"access_expiry_mins"`
	RefreshExpiryDays int    `koanf:"refresh_expiry_days"`
}

type WhatsAppConfig struct {
	WebhookVerifyToken string `koanf:"webhook_verify_token"`
	APIVersion         string `koanf:"api_version"`
	BaseURL            string `koanf:"base_url"` // Meta Graph API base URL
	AppID              string `koanf:"app_id"`   // WhatsApp App ID for frontend
	AppSecret          string `koanf:"app_secret"`
	ConfigID           string `koanf:"config_id"` // WhatsApp Config ID for frontend
}

type AIConfig struct {
	OpenAIKey    string `koanf:"openai_key"`
	AnthropicKey string `koanf:"anthropic_key"`
	GoogleKey    string `koanf:"google_key"`
}

type StorageConfig struct {
	Type      string `koanf:"type"` // local, s3
	LocalPath string `koanf:"local_path"`
	S3Bucket  string `koanf:"s3_bucket"`
	S3Region  string `koanf:"s3_region"`
	S3Key     string `koanf:"s3_key"`
	S3Secret  string `koanf:"s3_secret"`
}

// BackupConfig controls where backups are kept and how large an uploaded
// backup may be.
type BackupConfig struct {
	Dir         string `koanf:"dir"`
	MaxUploadMB int    `koanf:"max_upload_mb"`
}

type DefaultAdminConfig struct {
	Email    string `koanf:"email"`
	Password string `koanf:"password"`
	FullName string `koanf:"full_name"`
	// OrganizationName names the organization created with the first admin;
	// empty means "Default Organization".
	OrganizationName string `koanf:"organization_name"`
}

type CookieConfig struct {
	Domain string `koanf:"domain"` // Cookie domain (e.g., ".example.com"). Empty = current host.
	Secure bool   `koanf:"secure"` // Set Secure flag. Defaults to true when environment=production unless set.
}

type RateLimitConfig struct {
	Enabled             bool `koanf:"enabled"`
	LoginMaxAttempts    int  `koanf:"login_max_attempts"`
	RegisterMaxAttempts int  `koanf:"register_max_attempts"`
	RefreshMaxAttempts  int  `koanf:"refresh_max_attempts"`
	SSOMaxAttempts      int  `koanf:"sso_max_attempts"`
	WindowSeconds       int  `koanf:"window_seconds"`
	TrustProxy          bool `koanf:"trust_proxy"`
	APIMaxRequests      int  `koanf:"api_max_requests"`
	APIWindowSeconds    int  `koanf:"api_window_seconds"`
}

// Load loads configuration from file and environment variables
func Load(configPath string) (*Config, error) {
	k := koanf.New(".")

	// Load from config file if provided
	if configPath != "" {
		if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
			return nil, err
		}
	}

	// Load from environment variables (WHATOMATE_ prefix). A DOUBLE underscore
	// separates config levels; single underscores are preserved as part of the
	// key. This is required because both section and field names contain
	// underscores (e.g. default_admin, rate_limit, whatsapp.app_id) — collapsing
	// every "_" to "." would mangle them (whatsapp.app_id -> whatsapp.app.id), so
	// those keys could never be set via env.
	// e.g. WHATOMATE_DATABASE__HOST -> database.host
	//      WHATOMATE_WHATSAPP__APP_ID -> whatsapp.app_id
	//      WHATOMATE_DEFAULT_ADMIN__EMAIL -> default_admin.email
	if err := k.Load(env.Provider("WHATOMATE_", ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "WHATOMATE_")), "__", ".")
	}), nil); err != nil {
		return nil, err
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	// Set defaults
	setDefaults(&cfg, k.Exists("cookie.secure"))

	return &cfg, nil
}

// setDefaults fills unset values. cookieSecureSet reports whether
// cookie.secure was given, so an explicit false survives production.
func setDefaults(cfg *Config, cookieSecureSet bool) {
	if cfg.App.Name == "" {
		cfg.App.Name = "Whatomate"
	}
	if cfg.App.Environment == "" {
		cfg.App.Environment = "development"
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = DefaultPort
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 30
	}
	cfg.Database.Driver = strings.ToLower(strings.TrimSpace(cfg.Database.Driver))
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = DriverPostgres
	}
	if cfg.Database.Port == 0 {
		switch cfg.Database.Driver {
		case DriverMySQL:
			cfg.Database.Port = 3306
		case DriverSQLServer:
			cfg.Database.Port = 1433
		default:
			cfg.Database.Port = 5432
		}
	}
	if cfg.Database.Driver == DriverSQLite && cfg.Database.Path == "" {
		cfg.Database.Path = "./whatomate.db"
	}
	if cfg.Database.SSLMode == "" {
		cfg.Database.SSLMode = "disable"
	}
	if cfg.Database.MaxOpenConns == 0 {
		cfg.Database.MaxOpenConns = DefaultMaxOpenConns
	}
	if cfg.Database.MaxIdleConns == 0 {
		cfg.Database.MaxIdleConns = 5
	}
	if cfg.Database.ConnMaxLifetime == 0 {
		cfg.Database.ConnMaxLifetime = 300
	}
	if cfg.Redis.Port == 0 {
		cfg.Redis.Port = 6379
	}
	if cfg.JWT.AccessExpiryMins == 0 {
		cfg.JWT.AccessExpiryMins = 15
	}
	if cfg.JWT.RefreshExpiryDays == 0 {
		cfg.JWT.RefreshExpiryDays = 1
	}
	if cfg.WhatsApp.APIVersion == "" {
		cfg.WhatsApp.APIVersion = "v18.0"
	}
	if cfg.WhatsApp.BaseURL == "" {
		cfg.WhatsApp.BaseURL = "https://graph.facebook.com"
	}
	if cfg.Storage.Type == "" {
		cfg.Storage.Type = "local"
	}
	if cfg.Storage.LocalPath == "" {
		cfg.Storage.LocalPath = "./uploads"
	}
	// Default admin credentials (only used during initial setup)
	if cfg.DefaultAdmin.Email == "" {
		cfg.DefaultAdmin.Email = "admin@admin.com"
	}
	if cfg.DefaultAdmin.Password == "" {
		cfg.DefaultAdmin.Password = "admin"
	}
	if cfg.DefaultAdmin.FullName == "" {
		cfg.DefaultAdmin.FullName = "Admin"
	}
	// Cookie defaults
	if cfg.App.Environment == "production" && !cookieSecureSet {
		cfg.Cookie.Secure = true
	}
	// Rate limiting defaults
	if cfg.RateLimit.LoginMaxAttempts == 0 {
		cfg.RateLimit.LoginMaxAttempts = 10
	}
	if cfg.RateLimit.RegisterMaxAttempts == 0 {
		cfg.RateLimit.RegisterMaxAttempts = 10
	}
	if cfg.RateLimit.RefreshMaxAttempts == 0 {
		cfg.RateLimit.RefreshMaxAttempts = 30
	}
	if cfg.RateLimit.SSOMaxAttempts == 0 {
		cfg.RateLimit.SSOMaxAttempts = 10
	}
	if cfg.RateLimit.WindowSeconds == 0 {
		cfg.RateLimit.WindowSeconds = 60
	}
	if cfg.Retention.WebhookDeliveryDays == 0 {
		cfg.Retention.WebhookDeliveryDays = 90
	}
	// Calling defaults
	if cfg.Calling.MaxCallDuration == 0 {
		cfg.Calling.MaxCallDuration = 300
	}
	if cfg.Calling.AudioDir == "" {
		cfg.Calling.AudioDir = "./audio"
	}
	if cfg.Calling.HoldMusicFile == "" {
		cfg.Calling.HoldMusicFile = "hold_music.opus"
	}
	if cfg.Calling.TransferTimeoutSecs == 0 {
		cfg.Calling.TransferTimeoutSecs = 120
	}
	if cfg.Backup.Dir == "" {
		cfg.Backup.Dir = "backups"
	}
	if cfg.Backup.MaxUploadMB == 0 {
		cfg.Backup.MaxUploadMB = 10240
	}
}
