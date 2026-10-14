package testutil

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	testDB        *gorm.DB
	testDBOnce    sync.Once
	testDBInitErr error
)

// Driver returns TEST_DB_DRIVER, defaulting to postgres. It panics on any
// other value so a typo cannot silently run (or skip) the postgres suite.
func Driver() string {
	switch d := strings.ToLower(strings.TrimSpace(os.Getenv("TEST_DB_DRIVER"))); d {
	case "", config.DriverPostgres:
		return config.DriverPostgres
	case config.DriverSQLite, config.DriverMySQL:
		return d
	default:
		panic(fmt.Sprintf("TEST_DB_DRIVER=%q is not supported; use %q, %q or %q", d, config.DriverPostgres, config.DriverSQLite, config.DriverMySQL))
	}
}

// SetupTestDB creates a connection to the test database selected by TEST_DB_DRIVER.
// For sqlite, a fresh database file is created in a temporary directory.
// For postgres, TEST_DATABASE_URL must be set; if not, the test is skipped.
// For mysql, TEST_MYSQL_DSN must be set; if not, the test is skipped.
// Migrations are run only once across all tests to avoid conflicts.
func SetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	driver := Driver()
	if driver == config.DriverMySQL {
		return setupMySQLTestDB(t)
	}
	if driver == config.DriverSQLite {
		testDBOnce.Do(func() {
			dir, err := os.MkdirTemp("", "whatomate-test-*")
			if err != nil {
				testDBInitErr = err
				return
			}
			testDB, testDBInitErr = database.Open(&config.DatabaseConfig{Driver: driver, Path: filepath.Join(dir, "test.db")}, false)
			if testDBInitErr == nil {
				testDBInitErr = runMigrations(testDB)
			}
		})
		if testDBInitErr != nil {
			t.Fatalf("failed to initialize test database: %v", testDBInitErr)
		}
		return testDB.Session(&gorm.Session{})
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping database test")
	}

	// Initialize database and run migrations only once
	testDBOnce.Do(func() {
		var err error
		testDB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			testDBInitErr = fmt.Errorf("failed to connect to test postgres: %w", err)
			return
		}

		// Run migrations once
		if err := runMigrations(testDB); err != nil {
			testDBInitErr = fmt.Errorf("failed to run migrations: %w", err)
			return
		}

		// Clean up any existing data before tests start
		cleanupTables(testDB)
	})

	if testDBInitErr != nil {
		t.Fatalf("failed to initialize test database: %v", testDBInitErr)
	}

	// Return a new session for this test to avoid connection conflicts
	return testDB.Session(&gorm.Session{})
}

// setupMySQLTestDB connects to TEST_MYSQL_DSN, whose database name must start
// with whatomate_test: the DSN user may have server-wide privileges and the
// tables are truncated.
func setupMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN not set, skipping database test")
	}

	cfg, err := MySQLConfigFromDSN(raw)
	if err != nil {
		panic(err.Error())
	}
	testDBOnce.Do(func() {
		testDB, err = database.Open(cfg, false)
		if err != nil {
			testDBInitErr = fmt.Errorf("failed to connect to test mysql: %w", err)
			return
		}
		if err := runMigrations(testDB); err != nil {
			testDBInitErr = fmt.Errorf("failed to run migrations: %w", err)
			return
		}
		cleanupTables(testDB)
	})

	if testDBInitErr != nil {
		t.Fatalf("failed to initialize test database: %v", testDBInitErr)
	}
	return testDB.Session(&gorm.Session{})
}

// MySQLConfigFromDSN turns a go-sql-driver DSN such as TEST_MYSQL_DSN into a
// mysql DatabaseConfig. The database name must start with whatomate_test.
func MySQLConfigFromDSN(raw string) (*config.DatabaseConfig, error) {
	dsn, err := mysqldrv.ParseDSN(raw)
	if err != nil {
		return nil, fmt.Errorf("TEST_MYSQL_DSN does not parse: %w", err)
	}
	if !strings.HasPrefix(dsn.DBName, "whatomate_test") {
		return nil, fmt.Errorf("TEST_MYSQL_DSN names database %q; it must start with whatomate_test", dsn.DBName)
	}
	host, portStr, err := net.SplitHostPort(dsn.Addr)
	if err != nil {
		return nil, fmt.Errorf("TEST_MYSQL_DSN address: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("TEST_MYSQL_DSN port: %w", err)
	}
	return &config.DatabaseConfig{
		Driver: config.DriverMySQL, Host: host, Port: port,
		User: dsn.User, Password: dsn.Passwd, Name: dsn.DBName, SSLMode: "disable",
		MaxOpenConns: config.DefaultMaxOpenConns, MaxIdleConns: 5, ConnMaxLifetime: 300,
	}, nil
}

// PostgresConfigFromURL turns TEST_DATABASE_URL (postgres://user:pass@host:port/name?sslmode=…)
// into a postgres DatabaseConfig. The database name must start with whatomate_test.
func PostgresConfigFromURL(raw string) (*config.DatabaseConfig, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("TEST_DATABASE_URL does not parse: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !strings.HasPrefix(name, "whatomate_test") {
		return nil, fmt.Errorf("TEST_DATABASE_URL names database %q; it must start with whatomate_test", name)
	}
	port := 5432
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return nil, fmt.Errorf("TEST_DATABASE_URL port: %w", err)
		}
	}
	password, _ := u.User.Password()
	ssl := u.Query().Get("sslmode")
	if ssl == "" {
		ssl = "disable"
	}
	return &config.DatabaseConfig{
		Driver: config.DriverPostgres, Host: u.Hostname(), Port: port,
		User: u.User.Username(), Password: password, Name: name, SSLMode: ssl,
	}, nil
}

// SetupTestDBWithCleanup is like SetupTestDB but allows controlling cleanup behavior.
func SetupTestDBWithCleanup(t *testing.T, cleanup bool) *gorm.DB {
	t.Helper()

	db := SetupTestDB(t)

	if cleanup {
		t.Cleanup(func() {
			// Clean up only the data created by this test
			// Note: In parallel tests, this may affect other tests
			// Consider using unique identifiers instead
		})
	}

	return db
}

// runMigrations runs all model migrations, then creates the app's extra indexes.
func runMigrations(db *gorm.DB) error {
	if err := migrateModels(db); err != nil {
		return err
	}
	return database.CreateIndexes(db)
}

// migrateModels auto-migrates all models.
func migrateModels(db *gorm.DB) error {
	return db.AutoMigrate(
		// Core models
		&models.Organization{},
		&models.Permission{},
		&models.CustomRole{},
		&models.User{},
		&models.UserOrganization{},
		&models.Team{},
		&models.TeamMember{},
		&models.APIKey{},
		&models.SSOProvider{},
		&models.Webhook{},
		&models.WebhookDelivery{},
		&models.CustomAction{},
		&models.UserAvailabilityLog{},
		// WhatsApp models
		&models.WhatsAppAccount{},
		&models.Contact{},
		&models.Tag{},
		&models.ContactList{},
		&models.ContactListMember{},
		&models.GoogleCredential{},
		&models.UserPasskey{},
		&models.Message{},
		&models.Template{},
		&models.WhatsAppFlow{},
		// Chatbot models
		&models.ChatbotSettings{},
		&models.KeywordRule{},
		&models.ChatbotFlow{},
		&models.ChatbotFlowStep{},
		&models.ChatbotSession{},
		&models.ChatbotSessionMessage{},
		&models.AIContext{},
		&models.AgentTransfer{},
		// Bulk message models
		&models.BulkMessageCampaign{},
		&models.BulkMessageRecipient{},
		&models.NotificationRule{},
		// Catalog models
		&models.Catalog{},
		&models.CatalogProduct{},
		// Canned responses
		&models.CannedResponse{},
		// Dashboard
		&models.Widget{},
		// Conversation Notes
		&models.ConversationNote{},
		// Calling / IVR
		&models.CallLog{},
		&models.IVRFlow{},
		&models.CallTransfer{},
		&models.CallPermission{},
		// Audit
		&models.AuditLog{},
		// AI usage & shared Meta app
		&models.AIUsageLog{},
		&models.SharedMetaAppCredentials{},
		// Embedded store
		&models.KVEntry{},
		&models.ScheduledItem{},
		&models.CampaignJob{},
	)
}

// cleanupTables removes all data from tables.
// Uses TRUNCATE CASCADE on PostgreSQL to handle foreign key constraints properly.
func cleanupTables(db *gorm.DB) {
	tables := []string{
		"kv_entries",
		"scheduled_items",
		"campaign_jobs",
		"ai_usage_logs",
		"shared_meta_app_credentials",
		// Dashboard tables
		"widgets",
		// Catalog tables
		"catalog_products",
		"catalogs",
		// Canned responses
		"canned_responses",
		// Bulk message tables
		"bulk_message_recipients",
		"bulk_message_campaigns",
		"notification_rules",
		// Chatbot tables
		"chatbot_session_messages",
		"chatbot_sessions",
		"chatbot_flow_steps",
		"chatbot_flows",
		"keyword_rules",
		"chatbot_settings",
		"ai_contexts",
		"agent_transfers",
		// WhatsApp tables
		"messages",
		"tags",
		"contact_list_members",
		"contact_lists",
		"google_credentials",
		"contacts",
		"templates",
		"whatsapp_flows",
		"whatsapp_accounts",
		// Roles and permissions
		"role_permissions",
		"custom_roles",
		"permissions",
		// Core tables
		"team_members",
		"teams",
		"api_keys",
		"sso_providers",
		"webhook_deliveries",
		"webhooks",
		"custom_actions",
		"user_availability_logs",
		"user_organizations",
		"users",
		"organizations",
	}

	clearTables(db, tables)
}

// TruncateTables empties all tables (TRUNCATE on PostgreSQL and MySQL, DELETE on SQLite).
func TruncateTables(db *gorm.DB) {
	tables := []string{
		"kv_entries",
		"scheduled_items",
		"campaign_jobs",
		"widgets",
		"catalog_products",
		"catalogs",
		"canned_responses",
		"bulk_message_recipients",
		"bulk_message_campaigns",
		"notification_rules",
		"chatbot_session_messages",
		"chatbot_sessions",
		"chatbot_flow_steps",
		"chatbot_flows",
		"keyword_rules",
		"chatbot_settings",
		"ai_contexts",
		"agent_transfers",
		"messages",
		"tags",
		"contacts",
		"templates",
		"whatsapp_flows",
		"whatsapp_accounts",
		"role_permissions",
		"custom_roles",
		"permissions",
		"team_members",
		"teams",
		"api_keys",
		"sso_providers",
		"webhook_deliveries",
		"webhooks",
		"custom_actions",
		"user_availability_logs",
		"user_organizations",
		"users",
		"organizations",
	}

	clearTables(db, tables)
}

// clearTables empties tables, ignoring errors (e.g. a table not migrated).
func clearTables(db *gorm.DB, tables []string) {
	switch db.Name() {
	case config.DriverSQLite:
		// PRAGMA foreign_keys is a no-op inside a transaction, so pin one
		// connection and run the statements outside any transaction.
		_ = db.Connection(func(conn *gorm.DB) error {
			conn.Exec("PRAGMA foreign_keys = OFF")
			for _, table := range tables {
				conn.Exec(fmt.Sprintf("DELETE FROM %s", table))
			}
			conn.Exec("PRAGMA foreign_keys = ON")
			return nil
		})
	case config.DriverMySQL:
		// FOREIGN_KEY_CHECKS is per session, so pin one connection.
		_ = db.Connection(func(conn *gorm.DB) error {
			conn.Exec("SET FOREIGN_KEY_CHECKS = 0")
			for _, table := range tables {
				conn.Exec("TRUNCATE TABLE `" + table + "`")
			}
			conn.Exec("SET FOREIGN_KEY_CHECKS = 1")
			return nil
		})
	default:
		for _, table := range tables {
			db.Exec(fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
		}
	}
}
