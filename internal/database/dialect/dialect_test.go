package dialect_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/database/dialect"
)

func TestHelpers(t *testing.T) {
	pg, lite, my := dialect.Named("postgres"), dialect.Named("sqlite"), dialect.Named("mysql")
	cases := []struct {
		name                     string
		pg, lite, my             string
		wantPG, wantLite, wantMy string
	}{
		{"ILike", pg.ILike("name"), lite.ILike("name"), my.ILike("name"),
			"name ILIKE ?", "name LIKE ?", "LOWER(name) LIKE LOWER(?)"},
		{"AsText", pg.AsText("k"), lite.AsText("k"), my.AsText("k"),
			"k::text", "CAST(k AS TEXT)", "CAST(k AS CHAR)"},
		{"JSONText", pg.JSONText("m", "id"), lite.JSONText("m", "id"), my.JSONText("m", "id"),
			"m->>'id'", "json_extract(m, '$.id')", "JSON_UNQUOTE(JSON_EXTRACT(m, '$.id'))"},
		{"MinutesBetween", pg.MinutesBetween("a", "b"), lite.MinutesBetween("a", "b"), my.MinutesBetween("a", "b"),
			"EXTRACT(EPOCH FROM (b - a))/60", "((julianday(b) - julianday(a)) * 1440)", "(TIMESTAMPDIFF(MICROSECOND, a, b) / 60000000)"},
		{"DayOfWeek", pg.DayOfWeek("c"), lite.DayOfWeek("c"), my.DayOfWeek("c"),
			"EXTRACT(DOW FROM c)::int", "CAST(strftime('%w', c) AS INTEGER)", "(DAYOFWEEK(c) - 1)"},
		{"HourOf", pg.HourOf("c"), lite.HourOf("c"), my.HourOf("c"),
			"EXTRACT(HOUR FROM c)::int", "CAST(strftime('%H', c) AS INTEGER)", "HOUR(c)"},
		{"DateTrunc day", pg.DateTrunc("day", "c"), lite.DateTrunc("day", "c"), my.DateTrunc("day", "c"),
			"DATE_TRUNC('day', c)", "date(c)", "DATE(c)"},
		{"DateTrunc week", pg.DateTrunc("week", "c"), lite.DateTrunc("week", "c"), my.DateTrunc("week", "c"),
			"DATE_TRUNC('week', c)", "date(c, '-6 days', 'weekday 1')", "DATE(DATE_SUB(c, INTERVAL WEEKDAY(c) DAY))"},
		{"DateTrunc month", pg.DateTrunc("month", "c"), lite.DateTrunc("month", "c"), my.DateTrunc("month", "c"),
			"DATE_TRUNC('month', c)", "strftime('%Y-%m-01', c)", "CAST(DATE_FORMAT(c, '%Y-%m-01') AS DATE)"},
		{"Now", pg.Now(), lite.Now(), my.Now(), "NOW()", "CURRENT_TIMESTAMP", "UTC_TIMESTAMP(6)"},
		{"DescNullsLast", pg.DescNullsLast("c"), lite.DescNullsLast("c"), my.DescNullsLast("c"),
			"c DESC NULLS LAST", "c DESC NULLS LAST", "c DESC"},
		{"AscNullsFirst", pg.AscNullsFirst("c"), lite.AscNullsFirst("c"), my.AscNullsFirst("c"),
			"c ASC NULLS FIRST", "c ASC NULLS FIRST", "c ASC"},
	}
	for _, c := range cases {
		assert.Equal(t, c.wantPG, c.pg, c.name+" postgres")
		assert.Equal(t, c.wantLite, c.lite, c.name+" sqlite")
		assert.Equal(t, c.wantMy, c.my, c.name+" mysql")
	}
}

func TestJSONArrayHas(t *testing.T) {
	cond, arg := dialect.Named("postgres").JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "tags @> ?::jsonb", cond)
	assert.Equal(t, `["vip\"1"]`, arg)
	cond, arg = dialect.Named("sqlite").JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "EXISTS (SELECT 1 FROM json_each(tags) WHERE json_each.value = ?)", cond)
	assert.Equal(t, `vip"1`, arg)
}

func TestJSONArrayHas_MySQL(t *testing.T) {
	sql, arg := dialect.Named("mysql").JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "JSON_CONTAINS(tags, JSON_QUOTE(?))", sql)
	assert.Equal(t, `vip"1`, arg)
}

func TestCapabilities(t *testing.T) {
	for name, want := range map[string][2]bool{"postgres": {true, true}, "sqlite": {false, false}, "mysql": {true, false}} {
		d := dialect.Named(name)
		assert.Equal(t, want[0], d.SupportsSkipLocked(), name)
		assert.Equal(t, want[1], d.SupportsPercentileCont(), name)
	}
}

func TestNamed_RejectsUnknownDriver(t *testing.T) {
	assert.PanicsWithValue(t, `dialect: unsupported driver "oracle"`, func() { dialect.Named("oracle") })
}

func TestDateTrunc_RejectsUnknownUnit(t *testing.T) {
	for _, name := range []string{"postgres", "sqlite", "mysql"} {
		assert.PanicsWithValue(t, "dialect: unsupported DateTrunc unit year", func() { dialect.Named(name).DateTrunc("year", "c") }, name)
	}
}

func TestDBTimeScan(t *testing.T) {
	want := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, in := range []any{want, "2026-10-05", "2026-10-05 00:00:00", "2026-10-05T00:00:00Z", []byte("2026-10-05 00:00:00+00:00")} {
		var d dialect.DBTime
		require.NoError(t, d.Scan(in), "%v", in)
		assert.True(t, want.Equal(d.Time), "%v -> %v", in, d.Time)
	}

	cases := []struct {
		in   any
		want time.Time
	}{
		{"2026-10-12 09:30:00+02:00", time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC)},
		{"2026-10-12 09:30:00.123456", time.Date(2026, 10, 12, 9, 30, 0, 123456000, time.UTC)},
		{time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		var d dialect.DBTime
		require.NoError(t, d.Scan(c.in), "%v", c.in)
		assert.True(t, c.want.Equal(d.Time), "%v -> %v", c.in, d.Time)
	}

	var d dialect.DBTime
	require.NoError(t, d.Scan(nil))
	assert.True(t, d.IsZero())
	assert.Error(t, d.Scan("not a date"))
	assert.ErrorContains(t, d.Scan(42), "cannot scan int")
}

func TestDBTimeValueAndJSON(t *testing.T) {
	at := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	v, err := dialect.DBTime{Time: at}.Value()
	require.NoError(t, err)
	assert.Equal(t, at, v)

	got, err := json.Marshal(dialect.DBTime{Time: at})
	require.NoError(t, err)
	want, err := json.Marshal(at)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(got))
}
