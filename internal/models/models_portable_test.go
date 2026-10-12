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
	assert.Error(t, j.Scan("not json"))

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

	p := &models.UserPasskey{}
	l := &models.UserAvailabilityLog{}
	a := &models.AuditLog{}
	for _, tc := range []struct {
		m  interface{ BeforeCreate(*gorm.DB) error }
		id func() uuid.UUID
	}{
		{p, func() uuid.UUID { return p.ID }},
		{l, func() uuid.UUID { return l.ID }},
		{a, func() uuid.UUID { return a.ID }},
	} {
		require.NoError(t, tc.m.BeforeCreate(nil))
		assert.NotEqual(t, uuid.Nil, tc.id(), "%T", tc.m)
	}

	kept := &models.AuditLog{ID: keep}
	require.NoError(t, kept.BeforeCreate(nil))
	assert.Equal(t, keep, kept.ID)
}
