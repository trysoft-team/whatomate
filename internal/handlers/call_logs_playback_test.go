package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaskPlaybackRoundTrip(t *testing.T) {
	raw := []byte("OggS-local-recording")
	mask := []byte{1, 2, 3, 4, 5}
	masked := maskPlayback(raw, mask)
	assert.NotEqual(t, raw, masked)
	assert.Equal(t, raw, maskPlayback(masked, mask))
	assert.Equal(t, []byte{78, 101, 100, 87}, maskPlayback([]byte("OggS"), []byte{1, 2, 3, 4}))
}
