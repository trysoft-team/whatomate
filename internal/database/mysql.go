package database

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/trysoft/whatomate/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// Valid on MySQL 8 and MariaDB 10.6. Leaves out NO_ZERO_DATE/NO_ZERO_IN_DATE:
// the driver writes a zero time.Time as 0000-00-00 and reads it back as zero.
const mysqlSQLMode = "'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION'"

// MariaDB's JSON is an alias of LONGTEXT and reads back as longtext, so
// mapping to json there would alter the column on every migration.
var (
	mysqlTypes   = map[string]string{"uuid": "char(36)", "jsonb": "json", "text": "longtext"}
	mariaDBTypes = map[string]string{"uuid": "char(36)", "jsonb": "longtext", "text": "longtext"}
)

// An index key is at most 3072 bytes, and utf8mb4 counts 4 per character.
// These columns hold ASCII identifiers too long to index as utf8mb4; the
// binary collation also keeps them case-sensitive, as on PostgreSQL.
var mysqlColumnSuffixes = map[string]string{
	"user_passkeys.credential_id": " CHARACTER SET ascii COLLATE ascii_bin", // base64url, size 1400
}

// openMySQL connects, rejects unsupported servers and databases, and returns
// a dialector on that connection.
func openMySQL(cfg *config.DatabaseConfig) (gorm.Dialector, error) {
	dsn, err := mysqlDSN(cfg)
	if err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var version, charset string
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	if err := sqlDB.QueryRowContext(ctx, "SELECT VERSION(), @@character_set_database").Scan(&version, &charset); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to read server version: %w", err)
	}
	mariaDB, err := checkMySQLServer(version, charset)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	types := mysqlTypes
	if mariaDB {
		types = mariaDBTypes
	}
	six := 6
	return portableDialector{
		Dialector:      mysql.New(mysql.Config{Conn: sqlDB, ServerVersion: version, DefaultDatetimePrecision: &six}),
		types:          types,
		columnSuffixes: mysqlColumnSuffixes,
	}, nil
}

func mysqlDSN(cfg *config.DatabaseConfig) (string, error) {
	tls, err := mysqlTLS(cfg.SSLMode)
	if err != nil {
		return "", err
	}
	c := mysqldrv.NewConfig()
	c.User, c.Passwd = cfg.User, cfg.Password
	c.Net, c.Addr = "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	c.DBName = cfg.Name
	c.ParseTime, c.Loc = true, time.UTC
	c.ClientFoundRows = true // RowsAffected counts matched rows, as on PostgreSQL
	c.TLSConfig = tls
	c.Params = map[string]string{"time_zone": "'+00:00'", "sql_mode": mysqlSQLMode}
	return c.FormatDSN(), nil
}

// mysqlTLS maps the PostgreSQL-style ssl_mode values to the driver's tls param.
func mysqlTLS(mode string) (string, error) {
	switch mode {
	case "", "disable":
		return "false", nil
	case "require":
		return "skip-verify", nil
	case "verify-ca", "verify-full":
		return "true", nil
	case "preferred":
		return "preferred", nil
	}
	return "", fmt.Errorf("unsupported ssl_mode %q for mysql", mode)
}

var serverVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// checkMySQLServer reports whether version is MariaDB, and rejects MySQL
// before 8.0.13 (expression defaults), MariaDB before 10.6 (SKIP LOCKED)
// and databases whose character set is not utf8mb4.
func checkMySQLServer(version, charset string) (mariaDB bool, err error) {
	m := serverVersion.FindStringSubmatch(version)
	if m == nil {
		return false, fmt.Errorf("cannot parse server version %q", version)
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	atLeast := func(major, minor, patch int) bool {
		if v[0] != major {
			return v[0] > major
		}
		if v[1] != minor {
			return v[1] > minor
		}
		return v[2] >= patch
	}
	mariaDB = strings.Contains(version, "MariaDB")
	switch {
	case mariaDB && !atLeast(10, 6, 0):
		return true, fmt.Errorf("MariaDB 10.6 or later is required (server is %s)", version)
	case !mariaDB && !atLeast(8, 0, 13):
		return false, fmt.Errorf("MySQL 8.0.13 or later is required (server is %s)", version)
	case charset != "utf8mb4":
		return mariaDB, fmt.Errorf("database character set is %q; create it with CHARACTER SET utf8mb4", charset)
	}
	return mariaDB, nil
}

// mysqlMigrator writes literal defaults on JSON and TEXT columns as
// expression defaults, which MySQL requires, and treats the server's spelling
// of a default as equal to the model's, so boots don't re-alter.
type mysqlMigrator struct{ mysql.Migrator }

func (m mysqlMigrator) needsExprDefault(field *schema.Field) bool {
	switch m.Migrator.Migrator.DataTypeOf(field) {
	case "json", "longtext":
		return strings.HasPrefix(field.DefaultValue, "'")
	}
	return false
}

func (m mysqlMigrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	if m.needsExprDefault(field) {
		f := *field
		f.DefaultValue = "(" + field.DefaultValue + ")"
		return m.Migrator.FullDataTypeOf(&f)
	}
	return m.Migrator.FullDataTypeOf(field)
}

// AddColumn and AlterColumn are reimplemented for expression defaults because
// mysql.Migrator's versions call its own FullDataTypeOf, not this one.

func (m mysqlMigrator) AddColumn(value any, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		f := stmt.Schema.LookUpField(name)
		if f == nil || f.IgnoreMigration || f.PrimaryKey || !m.needsExprDefault(f) {
			return m.Migrator.AddColumn(value, name)
		}
		return m.DB.Exec("ALTER TABLE ? ADD ? ?", m.CurrentTable(stmt), clause.Column{Name: f.DBName}, m.FullDataTypeOf(f)).Error
	})
}

func (m mysqlMigrator) AlterColumn(value any, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		f := stmt.Schema.LookUpField(name)
		if f == nil || !m.needsExprDefault(f) {
			return m.Migrator.AlterColumn(value, name)
		}
		return m.DB.Exec("ALTER TABLE ? MODIFY COLUMN ? ?", m.CurrentTable(stmt), clause.Column{Name: f.DBName}, m.FullDataTypeOf(f)).Error
	})
}

// HasColumn accepts a bare table name; the generic version dereferences the
// statement's schema, which is nil for one.
func (m mysqlMigrator) HasColumn(value any, field string) bool {
	table, ok := value.(string)
	if !ok {
		return m.Migrator.HasColumn(value, field)
	}
	var count int64
	_ = m.DB.Raw("SELECT count(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
		table, field).Row().Scan(&count)
	return count > 0
}

func (m mysqlMigrator) MigrateColumn(value any, field *schema.Field, ct gorm.ColumnType) error {
	if dv, ok := ct.DefaultValue(); ok && dv != field.DefaultValue {
		if sameTypedDefault(field, dv) ||
			m.needsExprDefault(field) && normalizeMySQLDefault(dv) == normalizeMySQLDefault(field.DefaultValue) {
			ct = reportedDefault{columnType: ct, dv: field.DefaultValue}
		}
	}
	return m.Migrator.MigrateColumn(value, field, ct)
}

// sameTypedDefault reports whether the server's spelling of a bool or float
// default ("1", "0.70") has the model's value ("true", "0.7").
func sameTypedDefault(field *schema.Field, reported string) bool {
	switch v := field.DefaultValueInterface.(type) {
	case bool:
		b, err := strconv.ParseBool(reported)
		return err == nil && b == v
	case float64:
		f, err := strconv.ParseFloat(reported, 64)
		return err == nil && f == v
	}
	return false
}

// Embedded under another name: a field called ColumnType would hide the
// interface's ColumnType method.
type columnType = gorm.ColumnType

type reportedDefault struct {
	columnType
	dv string
}

func (c reportedDefault) DefaultValue() (string, bool) { return c.dv, true }

// normalizeMySQLDefault reduces the forms MySQL and MariaDB report a literal
// default in (_utf8mb4\'[]\', ('[]'), '[]') to the bare literal.
func normalizeMySQLDefault(s string) string {
	s = strings.ReplaceAll(strings.TrimPrefix(s, "_utf8mb4"), `\`, "")
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		s = s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		s = s[1 : len(s)-1]
	}
	return s
}
