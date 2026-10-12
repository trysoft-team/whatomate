package database_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/database/dialect"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

// The TEST_MYSQL_DSN user may hold server-wide DROP, so only databases this
// file created are ever dropped.
var mysqlScratchName = regexp.MustCompile(`^whatomate_test_[0-9a-f]{8}$`)

var mysqlAdmin = &config.DefaultAdminConfig{Email: "a@b.c", Password: "pw", FullName: "A"}

// createMySQLScratch creates an empty utf8mb4 database, drops it when t ends
// and returns its config.
func createMySQLScratch(t *testing.T) *config.DatabaseConfig {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN not set")
	}
	cfg, err := testutil.MySQLConfigFromDSN(raw)
	require.NoError(t, err)

	suffix := make([]byte, 4)
	_, err = rand.Read(suffix)
	require.NoError(t, err)
	name := "whatomate_test_" + hex.EncodeToString(suffix)

	admin, err := sql.Open("mysql", raw)
	require.NoError(t, err)
	_, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4")
	if err != nil {
		_ = admin.Close()
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		defer func() { _ = admin.Close() }()
		if !mysqlScratchName.MatchString(name) {
			t.Errorf("refusing to drop database %q", name)
			return
		}
		_, err := admin.Exec("DROP DATABASE `" + name + "`")
		assert.NoError(t, err)
	})
	cfg.Name = name
	return cfg
}

// openMySQLEmpty opens a fresh scratch database without migrating it.
func openMySQLEmpty(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Open(createMySQLScratch(t), false)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func migrateMySQL(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, database.RunMigrationWithProgress(db, mysqlAdmin))
}

func openMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	db := openMySQLEmpty(t)
	migrateMySQL(t, db)
	return db
}

func isMariaDB(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var version string
	require.NoError(t, db.Raw("SELECT VERSION()").Scan(&version).Error)
	return strings.Contains(version, "MariaDB")
}

func mysqlColumnType(t *testing.T, db *gorm.DB, table, column string) string {
	t.Helper()
	var typ string
	require.NoError(t, db.Raw(`SELECT column_type FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Row().Scan(&typ))
	return strings.ToLower(typ)
}

func TestOpenMySQL_MigratesAndSeeds(t *testing.T) {
	db := openMySQL(t)
	assert.Equal(t, "mysql", dialect.Of(db).Name())

	var n int64
	require.NoError(t, db.Model(&models.User{}).Count(&n).Error)
	assert.EqualValues(t, 1, n, "default admin seeded")
	require.NoError(t, db.Model(&models.UserOrganization{}).Count(&n).Error)
	assert.EqualValues(t, 1, n, "default admin linked to its organization")

	wantJSON := "json"
	if isMariaDB(t, db) {
		wantJSON = "longtext"
	}
	assert.Equal(t, "char(36)", mysqlColumnType(t, db, "contacts", "id"))
	assert.Equal(t, wantJSON, mysqlColumnType(t, db, "contacts", "tags"))
	assert.Equal(t, "longtext", mysqlColumnType(t, db, "messages", "content"))
	assert.Equal(t, "datetime(6)", mysqlColumnType(t, db, "contacts", "created_at"))

	assert.True(t, db.Migrator().HasColumn("users", "email"))
	assert.False(t, db.Migrator().HasColumn("users", "role"))
	assert.True(t, db.Migrator().HasColumn(&models.User{}, "Email"))
}

func TestOpenMySQL_RepeatMigrationAltersNothing(t *testing.T) {
	db := openMySQL(t)

	type column struct {
		TableName, ColumnName, ColumnType, IsNullable string
		ColumnDefault                                 *string
	}
	snapshot := func() []column {
		var cols []column
		require.NoError(t, db.Raw(`SELECT table_name AS table_name, column_name AS column_name, column_type AS column_type,
			is_nullable AS is_nullable, column_default AS column_default
			FROM information_schema.columns WHERE table_schema = DATABASE() ORDER BY table_name, column_name`).Scan(&cols).Error)
		return cols
	}

	before := snapshot()
	altered := captureMySQLDDL(t, db)
	migrateMySQL(t, db)
	assert.Empty(t, altered(), "second migration must not create, alter or drop tables or indexes")
	assert.Equal(t, before, snapshot())
}

// captureMySQLDDL records the DDL statements db runs from now on.
func captureMySQLDDL(t *testing.T, db *gorm.DB) func() []string {
	t.Helper()
	ddl := regexp.MustCompile(`(?i)^\s*(CREATE TABLE|ALTER TABLE|DROP TABLE|CREATE\s+(UNIQUE\s+)?INDEX|DROP INDEX)`)
	var mu sync.Mutex
	var statements []string
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:capture_ddl", func(tx *gorm.DB) {
		if sql := tx.Statement.SQL.String(); ddl.MatchString(sql) {
			mu.Lock()
			statements = append(statements, sql)
			mu.Unlock()
		}
	}))
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), statements...)
	}
}

func TestOpenMySQL_StringDefaultOnTextColumn(t *testing.T) {
	db := openMySQLEmpty(t)
	type textDefault struct {
		ID    uuid.UUID `gorm:"type:uuid;primaryKey"`
		Body  string    `gorm:"type:text;default:'hello'"`
		Label string    `gorm:"default:'x'"`
	}
	require.NoError(t, db.AutoMigrate(&textDefault{}))
	assert.Equal(t, "longtext", mysqlColumnType(t, db, "text_defaults", "body"))

	altered := captureMySQLDDL(t, db)
	require.NoError(t, db.AutoMigrate(&textDefault{}))
	assert.Empty(t, altered(), "second migration must not alter the table")

	id := uuid.New()
	require.NoError(t, db.Exec("INSERT INTO text_defaults (id) VALUES (?)", id).Error)
	var got textDefault
	require.NoError(t, db.First(&got, "id = ?", id).Error)
	assert.Equal(t, "hello", got.Body)
	assert.Equal(t, "x", got.Label)
}

func assertDuplicateKey(t *testing.T, err error) {
	t.Helper()
	var myErr *mysqldrv.MySQLError
	if assert.ErrorAs(t, err, &myErr) {
		assert.EqualValues(t, 1062, myErr.Number, myErr.Message)
	}
}

func TestOpenMySQL_PartialUniqueIndexes(t *testing.T) {
	db := openMySQL(t)
	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	user := models.User{OrganizationID: org.ID, Email: "u@example.com"}
	require.NoError(t, db.Create(&user).Error)
	team := models.Team{OrganizationID: org.ID, Name: "t"}
	require.NoError(t, db.Create(&team).Error)

	removed := models.TeamMember{TeamID: team.ID, UserID: user.ID}
	require.NoError(t, db.Create(&removed).Error)
	require.NoError(t, db.Delete(&removed).Error)
	require.NoError(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: user.ID}).Error, "re-adding a removed member")
	assertDuplicateKey(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: user.ID}).Error)
	var members []models.TeamMember
	require.NoError(t, db.Where("team_id = ?", team.ID).Find(&members).Error)
	assert.Len(t, members, 1)

	// user_organizations also has GORM's full unique index idx_user_org, so
	// only the active duplicate is checked here.
	require.NoError(t, db.Create(&models.UserOrganization{UserID: user.ID, OrganizationID: org.ID}).Error)
	assertDuplicateKey(t, db.Create(&models.UserOrganization{UserID: user.ID, OrganizationID: org.ID}).Error)
	var userOrgs []models.UserOrganization
	require.NoError(t, db.Where("user_id = ?", user.ID).Find(&userOrgs).Error)
	assert.Len(t, userOrgs, 1)

	newFlow := func(name string, active bool) error {
		f := models.IVRFlow{OrganizationID: org.ID, WhatsAppAccount: "acc", Name: name, IsCallStart: true}
		if err := db.Create(&f).Error; err != nil {
			return err
		}
		return db.Model(&f).Update("is_active", active).Error
	}
	require.NoError(t, newFlow("inactive 1", false))
	require.NoError(t, newFlow("inactive 2", false))
	require.NoError(t, newFlow("active 1", true))
	assertDuplicateKey(t, newFlow("active 2", true))
	var flows []models.IVRFlow
	require.NoError(t, db.Where("organization_id = ?", org.ID).Find(&flows).Error)
	assert.Len(t, flows, 3)
}

func TestOpenMySQL_IndexesIdempotent(t *testing.T) {
	db := openMySQL(t)
	require.NoError(t, database.CreateIndexes(db))
	require.NoError(t, database.CreateIndexes(db))

	var extra string
	require.NoError(t, db.Raw(`SELECT extra FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'team_members' AND column_name = 'idx_team_members_unique_active'`).Row().Scan(&extra))
	assert.Contains(t, strings.ToUpper(extra), "VIRTUAL")

	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	user := models.User{OrganizationID: org.ID, Email: "u@example.com"}
	require.NoError(t, db.Create(&user).Error)
	team := models.Team{OrganizationID: org.ID, Name: "t"}
	require.NoError(t, db.Create(&team).Error)
	m := models.TeamMember{TeamID: team.ID, UserID: user.ID}
	require.NoError(t, db.Create(&m).Error)
	var got models.TeamMember
	require.NoError(t, db.First(&got, "id = ?", m.ID).Error)
	assert.Equal(t, m.UserID, got.UserID)
}

func TestOpenMySQL_AddsAndAltersJSONColumnWithDefault(t *testing.T) {
	db := openMySQL(t)
	require.NoError(t, db.Exec("ALTER TABLE contacts DROP COLUMN tags").Error)
	require.NoError(t, db.Exec("ALTER TABLE contacts MODIFY COLUMN metadata varchar(100)").Error)
	require.NoError(t, db.AutoMigrate(&models.Contact{}))

	wantJSON := "json"
	if isMariaDB(t, db) {
		wantJSON = "longtext"
	}
	assert.Equal(t, wantJSON, mysqlColumnType(t, db, "contacts", "tags"))
	assert.Equal(t, wantJSON, mysqlColumnType(t, db, "contacts", "metadata"))

	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	c := models.Contact{OrganizationID: org.ID, PhoneNumber: "1"}
	require.NoError(t, db.Create(&c).Error)
	var tags, metadata string
	require.NoError(t, db.Raw("SELECT CAST(tags AS CHAR), CAST(metadata AS CHAR) FROM contacts WHERE id = ?", c.ID).Row().Scan(&tags, &metadata))
	assert.Equal(t, "[]", tags)
	assert.Equal(t, "{}", metadata)
}

func TestOpenMySQL_JSONDefaults(t *testing.T) {
	db := openMySQL(t)
	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	c := models.Contact{OrganizationID: org.ID, PhoneNumber: "1"}
	require.NoError(t, db.Create(&c).Error)
	assert.NotNil(t, c.Tags, "struct holds the default after Create")
	assert.Empty(t, c.Tags)
	assert.NotNil(t, c.Metadata, "struct holds the default after Create")

	var got models.Contact
	require.NoError(t, db.First(&got, "id = ?", c.ID).Error)
	assert.NotNil(t, got.Tags)
	assert.Empty(t, got.Tags)

	var raw string
	require.NoError(t, db.Raw("SELECT CAST(tags AS CHAR) FROM contacts WHERE id = ?", c.ID).Row().Scan(&raw))
	assert.Equal(t, "[]", raw)

	batch := []models.Contact{
		{OrganizationID: org.ID, PhoneNumber: "2"},
		{OrganizationID: org.ID, PhoneNumber: "3", Tags: models.JSONBArray{"vip"}},
	}
	require.NoError(t, db.Create(&batch).Error)
	assert.NotNil(t, batch[0].Tags)
	assert.Empty(t, batch[0].Tags)
	assert.Equal(t, models.JSONBArray{"vip"}, batch[1].Tags)
	require.NoError(t, db.Raw("SELECT CAST(tags AS CHAR) FROM contacts WHERE id = ?", batch[0].ID).Row().Scan(&raw))
	assert.Equal(t, "[]", raw)
}

func TestOpenMySQL_TimeArgsAreUTC(t *testing.T) {
	db := openMySQL(t)
	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	local := time.FixedZone("CAT", 2*3600)
	since := time.Now().In(local).Add(-time.Minute)
	var n int64
	require.NoError(t, db.Model(&models.Organization{}).Where("id = ? AND created_at >= ?", org.ID, since).Count(&n).Error)
	assert.EqualValues(t, 1, n)

	until := time.Now().In(local).Add(-time.Minute)
	require.NoError(t, db.Model(&models.Organization{}).Where("id = ? AND created_at < ?", org.ID, until).Count(&n).Error)
	assert.EqualValues(t, 0, n)
}

func TestOpenMySQL_TimePrecisionAndZero(t *testing.T) {
	db := openMySQLEmpty(t)
	type timeRow struct {
		ID uuid.UUID `gorm:"type:uuid;primaryKey"`
		At time.Time
	}
	require.NoError(t, db.AutoMigrate(&timeRow{}))

	at := time.Date(2026, 10, 12, 9, 30, 0, 123456000, time.UTC)
	precise := timeRow{ID: uuid.New(), At: at}
	zero := timeRow{ID: uuid.New()}
	require.NoError(t, db.Create(&precise).Error)
	require.NoError(t, db.Create(&zero).Error)

	var gotPrecise, gotZero timeRow
	require.NoError(t, db.First(&gotPrecise, "id = ?", precise.ID).Error)
	assert.True(t, at.Equal(gotPrecise.At), "got %s", gotPrecise.At)
	require.NoError(t, db.First(&gotZero, "id = ?", zero.ID).Error)
	assert.True(t, gotZero.At.IsZero(), "got %s", gotZero.At)
}

func TestOpenMySQL_BackfillLastInboundAt(t *testing.T) {
	checkBackfillLastInboundAt(t, openMySQL(t))
}

func TestOpenMySQL_RowsAffectedCountsMatchedRows(t *testing.T) {
	db := openMySQL(t)
	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	res := db.Exec("UPDATE organizations SET name = name WHERE id = ?", org.ID)
	require.NoError(t, res.Error)
	assert.EqualValues(t, 1, res.RowsAffected)
}

func TestOpenMySQL_ReadCommittedOnEveryConnection(t *testing.T) {
	db := openMySQLEmpty(t)
	// MariaDB 10.6 has no transaction_isolation alias.
	query := "SELECT @@transaction_isolation"
	if isMariaDB(t, db) {
		query = "SELECT @@tx_isolation"
	}

	require.NoError(t, db.Connection(func(conn *gorm.DB) error {
		var level string
		require.NoError(t, conn.Raw(query).Row().Scan(&level))
		assert.Equal(t, "READ-COMMITTED", level)
		return nil
	}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conns := make([]*sql.Conn, 4)
	for i := range conns {
		conns[i], err = sqlDB.Conn(ctx)
		require.NoError(t, err)
		defer func(c *sql.Conn) { _ = c.Close() }(conns[i])
	}
	for i, c := range conns {
		var level string
		require.NoError(t, c.QueryRowContext(ctx, query).Scan(&level))
		assert.Equal(t, "READ-COMMITTED", level, "connection %d", i)
	}
	assert.GreaterOrEqual(t, sqlDB.Stats().OpenConnections, len(conns))

	var wg sync.WaitGroup
	levels := make(chan string, 8)
	for i := 0; i < cap(levels); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var level string
			assert.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return tx.Raw(query).Row().Scan(&level)
			}))
			levels <- level
		}()
	}
	wg.Wait()
	close(levels)
	for level := range levels {
		assert.Equal(t, "READ-COMMITTED", level)
	}
}

func TestOpenMySQL_NestedTransactionRollsBackToSavepoint(t *testing.T) {
	db := openMySQL(t)
	errInner := errors.New("inner")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Create(&models.Organization{Name: "outer", Slug: "outer"}).Error)
		err := tx.Transaction(func(tx2 *gorm.DB) error {
			require.NoError(t, tx2.Create(&models.Organization{Name: "inner", Slug: "inner"}).Error)
			return errInner
		})
		assert.ErrorIs(t, err, errInner)
		return nil
	}))
	var slugs []string
	require.NoError(t, db.Model(&models.Organization{}).Where("slug IN ?", []string{"outer", "inner"}).Pluck("slug", &slugs).Error)
	assert.Equal(t, []string{"outer"}, slugs)
}
