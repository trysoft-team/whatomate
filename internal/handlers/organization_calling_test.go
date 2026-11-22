package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/trysoft/whatomate/internal/models"
)

func TestIncomingCallAllowed(t *testing.T) {
	t.Parallel()

	assert.True(t, incomingCallAllowed(models.JSONB{"calling_enabled": true}, false))
	assert.False(t, incomingCallAllowed(models.JSONB{"calling_enabled": false}, true))
	// Never saved: a call-start flow still answers. This is Mari Kumusha on production.
	assert.True(t, incomingCallAllowed(nil, true))
	assert.True(t, incomingCallAllowed(models.JSONB{}, true))
	assert.False(t, incomingCallAllowed(models.JSONB{}, false))
	assert.True(t, incomingCallAllowed(models.JSONB{"calling_enabled": "true"}, false))
	assert.False(t, incomingCallAllowed(models.JSONB{"calling_enabled": "false"}, true))
}
