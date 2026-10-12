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
	pg, lite := dialect.Named("postgres"), dialect.Named("sqlite")

	cases := []struct{ name, pg, lite string }{
		{"ILike", pg.ILike("name"), lite.ILike("name")},
		{"AsText", pg.AsText("keywords"), lite.AsText("keywords")},
		{"JSONText", pg.JSONText("metadata", "campaign_id"), lite.JSONText("metadata", "campaign_id")},
		{"MinutesBetween", pg.MinutesBetween("a", "b"), lite.MinutesBetween("a", "b")},
		{"DayOfWeek", pg.DayOfWeek("c"), lite.DayOfWeek("c")},
		{"HourOf", pg.HourOf("c"), lite.HourOf("c")},
		{"DateTrunc day", pg.DateTrunc("day", "c"), lite.DateTrunc("day", "c")},
		{"DateTrunc week", pg.DateTrunc("week", "c"), lite.DateTrunc("week", "c")},
		{"DateTrunc month", pg.DateTrunc("month", "c"), lite.DateTrunc("month", "c")},
		{"Now", pg.Now(), lite.Now()},
	}
	want := map[string][2]string{
		"ILike":           {"name ILIKE ?", "name LIKE ?"},
		"AsText":          {"keywords::text", "CAST(keywords AS TEXT)"},
		"JSONText":        {"metadata->>'campaign_id'", "json_extract(metadata, '$.campaign_id')"},
		"MinutesBetween":  {"EXTRACT(EPOCH FROM (b - a))/60", "((julianday(b) - julianday(a)) * 1440)"},
		"DayOfWeek":       {"EXTRACT(DOW FROM c)::int", "CAST(strftime('%w', c) AS INTEGER)"},
		"HourOf":          {"EXTRACT(HOUR FROM c)::int", "CAST(strftime('%H', c) AS INTEGER)"},
		"DateTrunc day":   {"DATE_TRUNC('day', c)", "date(c)"},
		"DateTrunc week":  {"DATE_TRUNC('week', c)", "date(c, '-6 days', 'weekday 1')"},
		"DateTrunc month": {"DATE_TRUNC('month', c)", "strftime('%Y-%m-01', c)"},
		"Now":             {"NOW()", "CURRENT_TIMESTAMP"},
	}
	for _, c := range cases {
		assert.Equal(t, want[c.name][0], c.pg, c.name+" postgres")
		assert.Equal(t, want[c.name][1], c.lite, c.name+" sqlite")
	}

	cond, arg := pg.JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "tags @> ?::jsonb", cond)
	assert.Equal(t, `["vip\"1"]`, arg)
	cond, arg = lite.JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "EXISTS (SELECT 1 FROM json_each(tags) WHERE json_each.value = ?)", cond)
	assert.Equal(t, `vip"1`, arg)

	assert.True(t, pg.SupportsSkipLocked())
	assert.False(t, lite.SupportsSkipLocked())
	assert.Panics(t, func() { pg.DateTrunc("year", "c") })
}

func TestDBTimeScan(t *testing.T) {
	want := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, in := range []any{want, "2026-10-05", "2026-10-05 00:00:00", "2026-10-05T00:00:00Z", []byte("2026-10-05 00:00:00+00:00")} {
		var d dialect.DBTime
		require.NoError(t, d.Scan(in), "%v", in)
		assert.True(t, want.Equal(d.Time), "%v -> %v", in, d.Time)
	}
	var d dialect.DBTime
	require.NoError(t, d.Scan(nil))
	assert.True(t, d.IsZero())
	assert.Error(t, d.Scan("not a date"))
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
