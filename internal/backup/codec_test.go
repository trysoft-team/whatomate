package backup

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type codecInner struct {
	Hours int  `gorm:"column:inner_hours"`
	On    bool `gorm:"column:inner_on"`
}

type codecSample struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey"`
	Ref     *uuid.UUID
	Name    string
	Flag    bool
	Count   int64
	Ratio   float64
	At      time.Time
	Maybe   *time.Time
	Deleted gorm.DeletedAt
	Doc     models.JSONB       `gorm:"type:jsonb"`
	List    models.JSONBArray  `gorm:"type:jsonb"`
	Tags    models.StringArray `gorm:"type:jsonb"`
	Raw     []byte
	Inner   codecInner `gorm:"embedded"`
}

func codecColumns(t *testing.T) []*schema.Field {
	t.Helper()
	s, err := schema.Parse(&codecSample{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	var cols []*schema.Field
	for _, n := range s.DBNames {
		cols = append(cols, s.FieldsByDBName[n])
	}
	return cols
}

func TestCodec_RoundTripsEveryKind(t *testing.T) {
	cols := codecColumns(t)
	ref := uuid.New()
	at := time.Date(2026, 10, 13, 9, 30, 15, 123456789, time.FixedZone("x", 2*3600))
	in := codecSample{
		ID: uuid.New(), Ref: &ref, Name: "héllo ✓ <b>", Flag: false, Count: 1 << 60, Ratio: 1.25,
		At: at, Maybe: nil, Deleted: gorm.DeletedAt{Time: at, Valid: true},
		Doc: models.JSONB{"a": "b", "n": float64(2)}, List: models.JSONBArray{"x", float64(1)},
		Tags: models.StringArray{"p", "q"}, Raw: []byte{0, 1, 255}, Inner: codecInner{Hours: 0, On: true},
	}
	line, err := encodeRow(context.Background(), cols, reflect.ValueOf(in))
	require.NoError(t, err)
	assert.Contains(t, string(line), `"at":"2026-10-13T07:30:15.123456789Z"`, "times are written in UTC")

	byName := map[string]*schema.Field{}
	for _, f := range cols {
		byName[f.DBName] = f
	}
	row, err := decodeRow(line, byName)
	require.NoError(t, err)
	assert.Equal(t, in.ID, row["id"])
	assert.Equal(t, &ref, row["ref"])
	assert.Equal(t, false, row["flag"])
	assert.Equal(t, int64(1<<60), row["count"])
	assert.True(t, at.Equal(row["at"].(time.Time)))
	assert.Nil(t, row["maybe"], "nil pointers decode to an untyped nil")
	assert.True(t, row["deleted"].(gorm.DeletedAt).Valid)
	assert.Equal(t, in.Doc, row["doc"])
	assert.Equal(t, in.List, row["list"])
	assert.Equal(t, in.Tags, row["tags"])
	assert.Equal(t, in.Raw, row["raw"])
	assert.Equal(t, 0, row["inner_hours"])
	assert.Equal(t, true, row["inner_on"])
}

func TestCodec_RejectsUnknownColumn(t *testing.T) {
	_, err := decodeRow([]byte(`{"nope":1}`), map[string]*schema.Field{})
	assert.ErrorContains(t, err, `unknown column "nope"`)
}
