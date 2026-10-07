package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	// flowMediaMaxBytes caps one decrypted Flow upload (Meta allows up to 100MB).
	flowMediaMaxBytes   = 100 << 20
	flowMediaParallel   = 4
	flowMediaStorageDir = "flow_media"
)

var flowMediaExtPattern = regexp.MustCompile(`^\.[a-z0-9]{1,7}$`)

// flowMediaCDNHosts are the domains WhatsApp serves Flow uploads from.
var flowMediaCDNHosts = []string{"whatsapp.net", "fbcdn.net", "fbsbx.com"}

// flowMediaURLAllowed guards CDN downloads: cdn_url comes from a request
// anyone holding the (public) key can encrypt, so only Meta CDNs are fetched.
var flowMediaURLAllowed = func(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("cdn_url must be an https URL")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range flowMediaCDNHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return fmt.Errorf("cdn_url host %q is not a WhatsApp CDN", host)
}

// flowMediaClient downloads CDN files; overridable in tests.
var flowMediaClient = func() *whatsapp.Client {
	return &whatsapp.Client{HTTPClient: &http.Client{
		Timeout:   flowEndpointRequestTTL,
		Transport: &http.Transport{DialContext: SSRFSafeDialer()},
	}}
}

// materializeFlowMedia replaces every PhotoPicker / DocumentPicker value in
// data with descriptors of the decrypted files saved to media storage.
// cache (media_id -> descriptor) avoids re-downloading files sent twice.
func (a *App) materializeFlowMedia(ctx context.Context, orgID uuid.UUID, data map[string]any, cache map[string]map[string]any) {
	for k, v := range data {
		list, ok := whatsapp.ParseFlowMediaList(v)
		if !ok {
			continue
		}
		data[k] = a.storeFlowMediaList(ctx, orgID, list, cache)
	}
}

func (a *App) storeFlowMediaList(ctx context.Context, orgID uuid.UUID, list []whatsapp.FlowMedia, cache map[string]map[string]any) []any {
	out := make([]any, len(list))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, flowMediaParallel)
	for i, m := range list {
		if cached, ok := cache[m.MediaID]; ok && m.MediaID != "" {
			out[i] = cached
			continue
		}
		wg.Add(1)
		go func(i int, m whatsapp.FlowMedia) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			desc := a.storeFlowMedia(ctx, orgID, m)
			mu.Lock()
			out[i] = desc
			if cache != nil && m.MediaID != "" && desc["error"] == nil {
				cache[m.MediaID] = desc
			}
			mu.Unlock()
		}(i, m)
	}
	wg.Wait()
	return out
}

// storeFlowMedia downloads, verifies, decrypts and saves one upload. Failures
// are reported in the descriptor so the rest of the submission still flows.
func (a *App) storeFlowMedia(ctx context.Context, orgID uuid.UUID, m whatsapp.FlowMedia) map[string]any {
	desc := map[string]any{"media_id": m.MediaID, "file_name": m.FileName}
	fail := func(err error) map[string]any {
		a.Log.Warn("flow media: failed to store upload", "media_id", m.MediaID, "file_name", m.FileName, "error", err)
		desc["error"] = err.Error()
		return desc
	}

	if err := flowMediaURLAllowed(m.CDNURL); err != nil {
		return fail(err)
	}
	enc, err := flowMediaClient().DownloadFlowMedia(ctx, m.CDNURL, flowMediaMaxBytes+64)
	if err != nil {
		return fail(err)
	}
	plain, err := whatsapp.DecryptFlowMedia(enc, m.EncryptionMetadata)
	if err != nil {
		return fail(err)
	}

	mimeType := mimeFromFilename(m.FileName)
	if mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(plain)
	}
	ext := strings.ToLower(filepath.Ext(m.FileName))
	if !flowMediaExtPattern.MatchString(ext) {
		ext = getExtensionFromMimeType(mimeType)
	}
	relDir := filepath.Join(flowMediaStorageDir, orgID.String())
	if err := a.ensureMediaDir(relDir); err != nil {
		return fail(fmt.Errorf("create media dir: %w", err))
	}
	name := uuid.New().String() + ext
	if err := os.WriteFile(filepath.Join(a.getMediaStoragePath(), relDir, name), plain, 0o644); err != nil {
		return fail(fmt.Errorf("save file: %w", err))
	}

	desc["mime_type"] = mimeType
	desc["size"] = len(plain)
	desc["path"] = filepath.ToSlash(filepath.Join(relDir, name))
	desc["url"] = fmt.Sprintf("%s/api/flow-media/%s/%s", strings.TrimRight(a.Config.Server.BasePath, "/"), orgID, name)
	return desc
}

// ServeFlowMedia serves a decrypted Flow upload to members of the owning
// organization (GET /api/flow-media/{org}/{name}).
func (a *App) ServeFlowMedia(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if fmt.Sprint(r.RequestCtx.UserValue("org")) != orgID.String() {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "File not found", nil, "")
	}
	name := filepath.Base(fmt.Sprint(r.RequestCtx.UserValue("name")))
	if _, err := uuid.Parse(strings.TrimSuffix(name, filepath.Ext(name))); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "File not found", nil, "")
	}

	path := filepath.Join(a.getMediaStoragePath(), flowMediaStorageDir, orgID.String(), name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "File not found", nil, "")
	}
	r.RequestCtx.Response.Header.Set("Content-Type", mimeFromFilename(name))
	r.RequestCtx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	r.RequestCtx.Response.Header.Set("Cache-Control", "private, max-age=3600")
	r.RequestCtx.SendFile(path)
	return nil
}
