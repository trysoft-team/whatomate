package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func photoPicker(name string, maxPhotos int) map[string]any {
	return map[string]any{"type": "PhotoPicker", "name": name, "label": "Photo", "max-uploaded-photos": float64(maxPhotos)}
}

func footerPayload(t *testing.T, screen any) map[string]any {
	t.Helper()
	children := screen.(map[string]any)["layout"].(map[string]any)["children"].([]any)
	for _, c := range children {
		comp := c.(map[string]any)
		if comp["type"] == "Footer" {
			return comp["on-click-action"].(map[string]any)["payload"].(map[string]any)
		}
	}
	t.Fatal("no footer")
	return nil
}

func TestFlowMediaURLAllowed(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"https://mmg.whatsapp.net/v/t62/x", "https://lookaside.fbsbx.com/a", "https://scontent.fbcdn.net/b"} {
		assert.NoError(t, handlers.FlowMediaURLAllowedForTest(ok), ok)
	}
	for _, bad := range []string{"http://mmg.whatsapp.net/x", "https://evil.com/x", "https://whatsapp.net.evil.com/x", "https://127.0.0.1/x", "file:///etc/passwd", ""} {
		assert.Error(t, handlers.FlowMediaURLAllowedForTest(bad), bad)
	}
}

func TestSanitizeScreens_MediaPayloadRules(t *testing.T) {
	t.Parallel()

	build := func(maxPhotos int) []any {
		return handlers.SanitizeScreensForMetaOptsForTest([]any{
			screen("UPLOAD",
				photoPicker("photo", maxPhotos),
				map[string]any{"type": "TextInput", "name": "name", "label": "Name"},
				footer(map[string]any{"name": "navigate", "next": map[string]any{"type": "screen", "name": "DONE"}})),
			screen("DONE", footer(map[string]any{"name": "complete", "payload": map[string]any{}})),
		}, false)
	}

	screens := build(1)
	nav := footerPayload(t, screens[0])
	assert.NotContains(t, nav, "photo", "media is not allowed in navigate")
	assert.Equal(t, "${form.name}", nav["name"])

	done := screens[1].(map[string]any)
	assert.NotContains(t, done["data"].(map[string]any), "photo")
	complete := footerPayload(t, done)
	assert.Equal(t, "${screen.UPLOAD.form.photo}", complete["photo"])
	assert.Equal(t, "${data.name}", complete["name"])

	complete = footerPayload(t, build(3)[1])
	assert.NotContains(t, complete, "photo", "complete can only carry single-file uploads")

	exchange := footerPayload(t, handlers.SanitizeScreensForMetaOptsForTest([]any{
		screen("UPLOAD", photoPicker("photo", 5), footer(dataExchange("NEXT"))),
		screen("NEXT", footer(dataExchange(""))),
	}, true)[1])
	assert.Equal(t, "${screen.UPLOAD.form.photo}", exchange["photo"])
}

func TestValidateFlowStructure_MediaRules(t *testing.T) {
	t.Parallel()

	two := []any{screen("A",
		photoPicker("photo", 1),
		map[string]any{"type": "DocumentPicker", "name": "doc", "label": "Doc", "max-uploaded-documents": float64(1)},
		footer(map[string]any{"name": "complete"}))}
	err := handlers.ValidateFlowStructureForTest(two, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one per screen")

	many := []any{screen("A", photoPicker("photo", 3), footer(map[string]any{"name": "complete"}))}
	err = handlers.ValidateFlowStructureForTest(many, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data endpoint")

	single := []any{screen("A", photoPicker("photo", 1), footer(map[string]any{"name": "complete"}))}
	require.NoError(t, handlers.ValidateFlowStructureForTest(single, false))

	manyEndpoint := []any{screen("A", photoPicker("photo", 3), footer(dataExchange("")))}
	require.NoError(t, handlers.ValidateFlowStructureForTest(manyEndpoint, true))
}

// Not parallel: swaps the package-level CDN client.
func TestFlowEndpoint_DecryptsUploadedMedia(t *testing.T) {
	plain := []byte("\xff\xd8\xff\xe0 fake jpeg bytes for an ID photo")
	cdnFile, meta, err := whatsapp.EncryptFlowMedia(plain)
	require.NoError(t, err)

	var hits atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/good":
			_, _ = w.Write(cdnFile)
		default:
			_, _ = w.Write([]byte("not the file"))
		}
	}))
	defer cdn.Close()
	t.Cleanup(handlers.UseFlowMediaServerForTest(cdn.Client()))

	f := newEndpointFixture(t, []any{
		screen("UPLOAD", photoPicker("photo", 2), footer(dataExchange("CONFIRM"))),
		screen("CONFIRM", footer(dataExchange(""))),
	}, "")
	f.app.Config.Storage.LocalPath = t.TempDir()
	token := "tok-" + uuid.New().String()

	media := func(id, path string) map[string]any {
		return map[string]any{
			"media_id":  id,
			"cdn_url":   cdn.URL + path,
			"file_name": "id-card.jpg",
			"encryption_metadata": map[string]any{
				"encrypted_hash": meta.EncryptedHash,
				"iv":             meta.IV,
				"encryption_key": meta.EncryptionKey,
				"hmac_key":       meta.HMACKey,
				"plaintext_hash": meta.PlaintextHash,
			},
		}
	}

	status, resp := f.call(t, map[string]any{
		"version": "3.0", "action": "data_exchange", "screen": "UPLOAD", "flow_token": token,
		"data": map[string]any{"photo": []any{media("m-good", "/good"), media("m-bad", "/tampered")}, "next_screen": "CONFIRM"},
	}, f.publicKey)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "CONFIRM", resp["screen"])

	// CONFIRM re-sends the same upload via ${screen.UPLOAD.form.photo}; the stored copy is reused.
	status, resp = f.call(t, map[string]any{
		"version": "3.0", "action": "data_exchange", "screen": "CONFIRM", "flow_token": token,
		"data": map[string]any{"photo": []any{media("m-good", "/good")}},
	}, f.publicKey)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.EqualValues(t, 2, hits.Load(), "good file fetched once, bad file once")

	params := resp["data"].(map[string]any)["extension_message_response"].(map[string]any)["params"].(map[string]any)
	photos := params["photo"].([]any)
	require.Len(t, photos, 1)
	good := photos[0].(map[string]any)
	assert.Equal(t, "m-good", good["media_id"])
	assert.Equal(t, "image/jpeg", good["mime_type"])
	assert.EqualValues(t, len(plain), good["size"])
	assert.Nil(t, good["error"])
	assert.Contains(t, good["url"], "/api/flow-media/"+f.flow.OrganizationID.String()+"/")

	saved, err := os.ReadFile(filepath.Join(f.app.Config.Storage.LocalPath, good["path"].(string)))
	require.NoError(t, err)
	assert.Equal(t, plain, saved)

	// Serving: owning org gets the file, other orgs get 404.
	name := filepath.Base(good["path"].(string))
	user := testutil.CreateTestUser(t, f.app.DB, f.flow.OrganizationID)
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, f.flow.OrganizationID, user.ID)
	testutil.SetPathParam(req, "org", f.flow.OrganizationID.String())
	testutil.SetPathParam(req, "name", name)
	require.NoError(t, f.app.ServeFlowMedia(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, "image/jpeg", string(req.RequestCtx.Response.Header.ContentType()))

	otherOrg := testutil.CreateTestOrganization(t, f.app.DB)
	otherUser := testutil.CreateTestUser(t, f.app.DB, otherOrg.ID)
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, otherOrg.ID, otherUser.ID)
	testutil.SetPathParam(req, "org", f.flow.OrganizationID.String())
	testutil.SetPathParam(req, "name", name)
	require.NoError(t, f.app.ServeFlowMedia(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}
