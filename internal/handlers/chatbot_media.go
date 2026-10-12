package handlers

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const maxChatbotMediaBytes = 16 << 20 // 16MB

var chatbotMediaAllowedMIME = map[string]bool{
	"image/jpeg":         true,
	"image/png":          true,
	"image/webp":         true,
	"image/gif":          true,
	"audio/mpeg":         true,
	"audio/mp4":          true,
	"audio/aac":          true,
	"audio/ogg":          true,
	"audio/opus":         true,
	"audio/amr":          true,
	"video/mp4":          true,
	"video/3gpp":         true,
	"application/pdf":    true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"text/plain": true,
}

// UploadChatbotMedia stores a file for use as a Message-node media_url and
// returns an absolute public HTTPS URL Meta can fetch.
func (a *App) UploadChatbotMedia(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceFlowsChatbot, models.ActionWrite)
	if err != nil {
		return nil
	}

	form, err := r.RequestCtx.MultipartForm()
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid multipart form: "+err.Error(), nil, "")
	}
	files := form.File["file"]
	if len(files) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No file provided", nil, "")
	}
	fileHeader := files[0]
	file, err := fileHeader.Open()
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Failed to open file", nil, "")
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxChatbotMediaBytes+1))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read file", nil, "")
	}
	if len(data) > maxChatbotMediaBytes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "File too large. Maximum size is 16MB", nil, "")
	}

	mimeType := fileHeader.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = mimeFromFilename(fileHeader.Filename)
	}
	if !chatbotMediaAllowedMIME[mimeType] {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unsupported media type: "+mimeType, nil, "")
	}

	id := uuid.New()
	ext := getExtensionFromMimeType(mimeType)
	if ext == "" {
		ext = filepath.Ext(fileHeader.Filename)
	}
	relDir := filepath.Join("chatbot", orgID.String())
	if err := a.ensureMediaDir(relDir); err != nil {
		a.Log.Error("Failed to create chatbot media dir", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create media directory", nil, "")
	}
	filename := id.String() + ext
	absPath := filepath.Join(a.getMediaStoragePath(), relDir, filename)
	if err := os.WriteFile(absPath, data, 0644); err != nil {
		a.Log.Error("Failed to write chatbot media", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}

	// Sidecar mime for public serve
	_ = os.WriteFile(absPath+".mime", []byte(mimeType), 0644)

	publicURL := a.chatbotMediaPublicURL(r, id.String()+ext)
	if publicURL == "" {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
			"Could not determine public origin for media URL", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"url":       publicURL,
		"id":        id.String(),
		"filename":  fileHeader.Filename,
		"mime_type": mimeType,
		"size":      len(data),
	})
}

// ServeChatbotMedia is a public (no-auth) endpoint so Meta can download
// Message-node media by link.
func (a *App) ServeChatbotMedia(r *fastglue.Request) error {
	name := r.RequestCtx.UserValue("id").(string)
	name = filepath.Base(name)
	if name == "" || name == "." || strings.Contains(name, "..") {
		r.RequestCtx.SetStatusCode(fasthttp.StatusBadRequest)
		return nil
	}

	// Search under chatbot/*/name — org isolation is UUID obscurity on the filename.
	root := filepath.Join(a.getMediaStoragePath(), "chatbot")
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.Name() == name {
			found = path
			return io.EOF // stop walk
		}
		return nil
	})
	if found == "" {
		r.RequestCtx.SetStatusCode(fasthttp.StatusNotFound)
		return nil
	}

	mimeType := "application/octet-stream"
	if b, err := os.ReadFile(found + ".mime"); err == nil {
		mimeType = strings.TrimSpace(string(b))
	}
	r.RequestCtx.Response.Header.Set("Content-Type", mimeType)
	r.RequestCtx.Response.Header.Set("Cache-Control", "public, max-age=86400")
	r.RequestCtx.SendFile(found)
	return nil
}

// chatbotMediaPublicURL builds an absolute URL Meta can fetch. Prefer the
// browser request origin (same as the webhook "location" URL shown in
// account settings); optional server.public_url overrides when set.
func (a *App) chatbotMediaPublicURL(r *fastglue.Request, filename string) string {
	prefix := strings.TrimRight(a.Config.Server.BasePath, "/")
	return a.publicAbsoluteURL(r, fmt.Sprintf("%s/public/chatbot-media/%s", prefix, filename))
}

// publicAbsoluteURL prefixes path with server.public_url when set; otherwise
// with the browser location the user opened Whatomate at (as the webhook URL
// in account settings is), falling back to the request host.
func (a *App) publicAbsoluteURL(r *fastglue.Request, path string) string {
	if base := strings.TrimRight(a.Config.Server.PublicURL, "/"); base != "" {
		return base + path
	}
	if r == nil || r.RequestCtx == nil {
		return ""
	}
	if origin := browserOrigin(r); origin != "" {
		return origin + path
	}

	scheme := "https"
	if xf := string(r.RequestCtx.Request.Header.Peek("X-Forwarded-Proto")); xf != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.Split(xf, ",")[0]))
	} else if !r.RequestCtx.IsTLS() && a.Config.App.Environment == "development" {
		scheme = "http"
	}
	host := string(r.RequestCtx.Host())
	if xh := string(r.RequestCtx.Request.Header.Peek("X-Forwarded-Host")); xh != "" {
		host = strings.TrimSpace(strings.Split(xh, ",")[0])
	}
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}

// browserOrigin returns the scheme://host the browser is on, from the Origin
// or Referer header. The dev proxy rewrites Host, but not these.
func browserOrigin(r *fastglue.Request) string {
	for _, h := range []string{"Origin", "Referer"} {
		raw := strings.TrimSpace(string(r.RequestCtx.Request.Header.Peek(h)))
		if raw == "" || raw == "null" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		return u.Scheme + "://" + u.Host
	}
	return ""
}

func mimeFromFilename(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".aac":
		return "audio/aac"
	case ".mp4":
		return "video/mp4"
	case ".3gp":
		return "video/3gpp"
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}
