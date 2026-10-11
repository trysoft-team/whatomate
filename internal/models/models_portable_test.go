package models_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
)

func TestJSONTypesScanStringAndBytes(t *testing.T) {
	var j models.JSONB
	require.NoError(t, j.Scan(`{"a":1}`))
	assert.Equal(t, float64(1), j["a"])
	require.NoError(t, j.Scan([]byte(`{"a":2}`)))
	assert.Equal(t, float64(2), j["a"])

	var arr models.JSONBArray
	require.NoError(t, arr.Scan(`["x"]`))
	assert.Equal(t, models.JSONBArray{"x"}, arr)

	var s models.StringArray
	require.NoError(t, s.Scan(`["y"]`))
	assert.Equal(t, models.StringArray{"y"}, s)

	v, err := models.JSONB{"k": "v"}.Value()
	require.NoError(t, err)
	assert.IsType(t, "", v, "Value returns string so text/nvarchar columns accept it")
}

func TestBeforeCreateAssignsID(t *testing.T) {
	var b models.BaseModel
	require.NoError(t, b.BeforeCreate(nil))
	assert.NotEqual(t, uuid.Nil, b.ID)

	keep := uuid.New()
	b2 := models.BaseModel{ID: keep}
	require.NoError(t, b2.BeforeCreate(nil))
	assert.Equal(t, keep, b2.ID)

	for _, m := range []interface{ BeforeCreate(*gorm.DB) error }{&models.UserPasskey{}, &models.UserAvailabilityLog{}, &models.AuditLog{}} {
		require.NoError(t, m.BeforeCreate(nil))
	}
}
