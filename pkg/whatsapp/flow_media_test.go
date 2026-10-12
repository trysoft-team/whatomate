package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowMedia_RoundTrip(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 1, 15, 16, 17, 4096} {
		plain := make([]byte, size)
		for i := range plain {
			plain[i] = byte(i * 7)
		}
		cdnFile, meta, err := EncryptFlowMedia(plain)
		require.NoError(t, err)
		got, err := DecryptFlowMedia(cdnFile, meta)
		require.NoError(t, err, "size %d", size)
		assert.Equal(t, plain, got)
	}
}

func TestFlowMedia_RejectsTampering(t *testing.T) {
	t.Parallel()

	cdnFile, meta, err := EncryptFlowMedia([]byte("passport scan"))
	require.NoError(t, err)

	tampered := append([]byte{}, cdnFile...)
	tampered[0] ^= 0xFF
	_, err = DecryptFlowMedia(tampered, meta)
	assert.True(t, errors.Is(err, ErrFlowMediaIntegrity), "encrypted hash: %v", err)

	// A matching encrypted hash but wrong HMAC key must still fail.
	_, other, err := EncryptFlowMedia([]byte("x"))
	require.NoError(t, err)
	bad := meta
	bad.HMACKey = other.HMACKey
	_, err = DecryptFlowMedia(cdnFile, bad)
	assert.True(t, errors.Is(err, ErrFlowMediaIntegrity), "hmac: %v", err)

	bad = meta
	bad.PlaintextHash = other.PlaintextHash
	_, err = DecryptFlowMedia(cdnFile, bad)
	assert.True(t, errors.Is(err, ErrFlowMediaIntegrity), "plaintext hash: %v", err)
}

func TestParseFlowMediaList(t *testing.T) {
	t.Parallel()

	v := []any{map[string]any{
		"media_id":            "m1",
		"cdn_url":             "https://mmg.whatsapp.net/x",
		"file_name":           "IMG.jpg",
		"encryption_metadata": map[string]any{"iv": "aXY="},
	}}
	list, ok := ParseFlowMediaList(v)
	require.True(t, ok)
	require.Len(t, list, 1)
	assert.Equal(t, "m1", list[0].MediaID)
	assert.Equal(t, "aXY=", list[0].EncryptionMetadata.IV)

	for _, notMedia := range []any{
		"text", []any{}, []any{"a"},
		[]any{map[string]any{"cdn_url": "https://x"}},
		[]any{map[string]any{"file_name": "a.pdf", "media_id": "1"}},
	} {
		_, ok := ParseFlowMediaList(notMedia)
		assert.False(t, ok, "%v", notMedia)
	}
}

func TestDownloadFlowMedia_EnforcesLimit(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client()}
	data, err := c.DownloadFlowMedia(context.Background(), srv.URL, 10)
	require.NoError(t, err)
	assert.Equal(t, "0123456789", string(data))

	_, err = c.DownloadFlowMedia(context.Background(), srv.URL, 9)
	assert.Error(t, err)
}
