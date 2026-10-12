package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
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

// openMySQL rejects unsupported servers and databases, then returns a
// dialector on a pool whose connections run at READ COMMITTED.
func openMySQL(cfg *config.DatabaseConfig) (gorm.Dialector, error) {
	mariaDB, err := probeMySQL(cfg)
	if err != nil {
		return nil, err
	}
	dsn, err := mysqlDSN(cfg, mysqlIsolationVar(mariaDB))
	if err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	types := mysqlTypes
	if mariaDB {
		types = mariaDBTypes
	}
	six := 6
	return portableDialector{
		// Initialize reads VERSION() itself: it sets MariaDB-specific flags
		// such as DontSupportNullAsDefaultValue from it.
		Dialector:      mysql.New(mysql.Config{Conn: sqlDB, DefaultDatetimePrecision: &six}),
		types:          types,
		columnSuffixes: mysqlColumnSuffixes,
		pool:           sqlDB,
	}, nil
}

// probeMySQL checks the server and database on a throwaway connection and
// reports whether the server is MariaDB.
func probeMySQL(cfg *config.DatabaseConfig) (mariaDB bool, err error) {
	dsn, err := mysqlDSN(cfg, "")
	if err != nil {
		return false, err
	}
	probe, err := sql.Open("mysql", dsn)
	if err != nil {
		return false, err
	}
	defer func() { _ = probe.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probe.PingContext(ctx); err != nil {
		return false, fmt.Errorf("failed to connect to database: %w", err)
	}
	var version, charset string
	if err := probe.QueryRowContext(ctx, "SELECT VERSION(), @@character_set_database").Scan(&version, &charset); err != nil {
		return false, fmt.Errorf("failed to read server version: %w", err)
	}
	return checkMySQLServer(version, charset)
}

// mysqlIsolationVar names the session isolation variable: MySQL 8 knows only
// transaction_isolation, MariaDB 10.6 only tx_isolation. InnoDB defaults to
// REPEATABLE READ, whose gap locks can block or deadlock locking reads (SKIP
// LOCKED pickups, DELETE ... LIMIT prunes) that PostgreSQL's READ COMMITTED
// lets through.
func mysqlIsolationVar(mariaDB bool) string {
	if mariaDB {
		return "tx_isolation"
	}
	return "transaction_isolation"
}

// mysqlDSN builds the connection DSN; if isolationVar is set, every new
// connection sets it to READ-COMMITTED.
func mysqlDSN(cfg *config.DatabaseConfig, isolationVar string) (string, error) {
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
	if isolationVar != "" {
		c.Params[isolationVar] = "'READ-COMMITTED'"
	}
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
// before 8.0.19 (gorm.io/driver/mysql uses DROP CONSTRAINT), MariaDB before
// 10.6 (SKIP LOCKED) and databases whose character set is not utf8mb4.
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
	case !mariaDB && !atLeast(8, 0, 19):
		return false, fmt.Errorf("MySQL 8.0.19 or later is required (server is %s)", version)
	case charset != "utf8mb4":
		return mariaDB, fmt.Errorf("database character set is %q; create it with CHARACTER SET utf8mb4", charset)
	}
	return mariaDB, nil
}

// fillLiteralJSONDefaults sets zero-valued fields whose default is a JSON
// literal ('[]', '{}') to that value, so the INSERT writes it. GORM otherwise
// leaves them out and reads the database default back through RETURNING,
// which MySQL lacks, so the struct would keep nil while the row holds [].
func fillLiteralJSONDefaults(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}
	type literal struct {
		field *schema.Field
		json  []byte
	}
	var literals []literal
	for _, f := range db.Statement.Schema.FieldsWithDefaultDBValue {
		v := f.DefaultValue
		if len(v) < 2 || v[0] != '\'' || v[len(v)-1] != '\'' {
			continue
		}
		v = v[1 : len(v)-1]
		if (strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{")) && json.Valid([]byte(v)) {
			literals = append(literals, literal{f, []byte(v)})
		}
	}
	if len(literals) == 0 {
		return
	}

	ctx := db.Statement.Context
	fill := func(rv reflect.Value) {
		if rv.Kind() != reflect.Struct || !rv.CanAddr() {
			return
		}
		for _, l := range literals {
			if _, zero := l.field.ValueOf(ctx, rv); !zero {
				continue
			}
			v := reflect.New(l.field.FieldType)
			if json.Unmarshal(l.json, v.Interface()) != nil {
				continue
			}
			_ = db.AddError(l.field.Set(ctx, rv, v.Elem().Interface()))
		}
	}
	switch rv := db.Statement.ReflectValue; rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			fill(reflect.Indirect(rv.Index(i)))
		}
	case reflect.Struct:
		fill(rv)
	}
}

// mysqlMigrator writes literal defaults on JSON and TEXT columns as
// expression defaults, which MySQL requires, and treats the server's spelling
// of a default as equal to the model's, so boots don't re-alter.
type mysqlMigrator struct{ mysql.Migrator }

func (m mysqlMigrator) needsExprDefault(field *schema.Field) bool {
	switch m.Migrator.Migrator.DataTypeOf(field) {
	case "json", "longtext":
		// GORM strips the quotes from a string field's default.
		_, isString := field.DefaultValueInterface.(string)
		return isString || strings.HasPrefix(field.DefaultValue, "'")
	}
	return false
}

func (m mysqlMigrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	if m.needsExprDefault(field) {
		f := *field
		if s, ok := field.DefaultValueInterface.(string); ok {
			f.DefaultValueInterface = nil
			f.DefaultValue = "(" + mysqlStringLiteral(s) + ")"
		} else {
			f.DefaultValue = "(" + field.DefaultValue + ")"
		}
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

// mysqlStringLiteral quotes s for SQL; backslash escapes are on in mysqlSQLMode.
func mysqlStringLiteral(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}

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
