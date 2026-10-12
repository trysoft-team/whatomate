package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scopes for Sheets + Calendar + Drive + Docs (service account).
var defaultScopes = []string{
	"https://www.googleapis.com/auth/spreadsheets",
	"https://www.googleapis.com/auth/calendar",
	"https://www.googleapis.com/auth/drive",
	"https://www.googleapis.com/auth/documents",
}

// OAuthScopes are requested when a user connects their own Google account.
// Drive is limited to files the app creates or the user opens with it, which
// keeps the app out of Google's "restricted" scope review.
var OAuthScopes = []string{
	"openid",
	"email",
	"https://www.googleapis.com/auth/spreadsheets",
	"https://www.googleapis.com/auth/calendar.events",
	"https://www.googleapis.com/auth/documents",
	"https://www.googleapis.com/auth/drive.file",
}

// Credential JSON types accepted by NewClient.
const (
	CredTypeServiceAccount = "service_account"
	CredTypeAuthorizedUser = "authorized_user"
)

// AuthorizedUser is Google's "authorized_user" credential format: an OAuth
// client plus a user's refresh token.
type AuthorizedUser struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

// OAuthEndpoint is Google's OAuth endpoint; tests may point it elsewhere.
var OAuthEndpoint = google.Endpoint

// Client talks to Google Workspace APIs with a service account or a
// connected user account.
type Client struct {
	http *http.Client
}

// NewClient builds an authenticated client from service-account or
// authorized_user JSON bytes. Other credential types (e.g. external_account)
// are rejected because they can make the server fetch arbitrary URLs.
func NewClient(ctx context.Context, credJSON []byte) (*Client, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(credJSON, &head); err != nil {
		return nil, fmt.Errorf("parse google credential: %w", err)
	}
	switch head.Type {
	case CredTypeServiceAccount:
		cfg, err := google.JWTConfigFromJSON(credJSON, defaultScopes...)
		if err != nil {
			return nil, fmt.Errorf("parse service account: %w", err)
		}
		return &Client{http: cfg.Client(ctx)}, nil
	case CredTypeAuthorizedUser:
		var au AuthorizedUser
		if err := json.Unmarshal(credJSON, &au); err != nil {
			return nil, fmt.Errorf("parse authorized user: %w", err)
		}
		if au.ClientID == "" || au.RefreshToken == "" {
			return nil, fmt.Errorf("authorized user credential is missing client_id or refresh_token")
		}
		cfg := &oauth2.Config{
			ClientID:     au.ClientID,
			ClientSecret: au.ClientSecret,
			Endpoint:     OAuthEndpoint,
			Scopes:       OAuthScopes,
		}
		return &Client{http: cfg.Client(ctx, &oauth2.Token{RefreshToken: au.RefreshToken})}, nil
	default:
		return nil, fmt.Errorf("unsupported google credential type %q", head.Type)
	}
}

// NewSheetsClient is an alias kept for callers that only need Sheets.
func NewSheetsClient(ctx context.Context, saJSON []byte) (*Client, error) {
	return NewClient(ctx, saJSON)
}

func (c *Client) doJSON(ctx context.Context, method, rawURL string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func checkStatus(code int, data []byte, op string) error {
	if code >= 200 && code < 300 {
		return nil
	}
	return fmt.Errorf("%s %d: %s", op, code, strings.TrimSpace(string(data)))
}

// --- Sheets ---

// AppendRows appends values to sheetName (A1 range = sheet name).
func (c *Client) AppendRows(ctx context.Context, spreadsheetID, sheetName string, rows [][]any) error {
	if spreadsheetID == "" {
		return fmt.Errorf("spreadsheet_id required")
	}
	rangeA1 := sheetName
	if rangeA1 == "" {
		rangeA1 = "Sheet1"
	}
	u := fmt.Sprintf(
		"https://sheets.googleapis.com/v4/spreadsheets/%s/values/%s:append?valueInputOption=USER_ENTERED&insertDataOption=INSERT_ROWS",
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeA1),
	)
	data, code, err := c.doJSON(ctx, http.MethodPost, u, map[string]any{"values": rows})
	if err != nil {
		return err
	}
	return checkStatus(code, data, "sheets append")
}

// ReadRange returns values for an A1 range (e.g. "Sheet1!A1:D10").
func (c *Client) ReadRange(ctx context.Context, spreadsheetID, rangeA1 string) ([][]any, error) {
	if spreadsheetID == "" || rangeA1 == "" {
		return nil, fmt.Errorf("spreadsheet_id and range required")
	}
	u := fmt.Sprintf(
		"https://sheets.googleapis.com/v4/spreadsheets/%s/values/%s",
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeA1),
	)
	data, code, err := c.doJSON(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(code, data, "sheets read"); err != nil {
		return nil, err
	}
	var parsed struct {
		Values [][]any `json:"values"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.Values, nil
}

// UpdateRange writes values to an A1 range.
func (c *Client) UpdateRange(ctx context.Context, spreadsheetID, rangeA1 string, rows [][]any) error {
	if spreadsheetID == "" || rangeA1 == "" {
		return fmt.Errorf("spreadsheet_id and range required")
	}
	u := fmt.Sprintf(
		"https://sheets.googleapis.com/v4/spreadsheets/%s/values/%s?valueInputOption=USER_ENTERED",
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeA1),
	)
	data, code, err := c.doJSON(ctx, http.MethodPut, u, map[string]any{"values": rows})
	if err != nil {
		return err
	}
	return checkStatus(code, data, "sheets update")
}

// --- Calendar ---

// CalendarEvent is a minimal Calendar event payload.
type CalendarEvent struct {
	ID          string
	Summary     string
	Description string
	Start       string // RFC3339 or date
	End         string
	TimeZone    string
	Attendees   []string
}

// CreateEvent inserts a calendar event and returns its id.
func (c *Client) CreateEvent(ctx context.Context, calendarID string, ev CalendarEvent) (string, error) {
	if calendarID == "" {
		calendarID = "primary"
	}
	body := map[string]any{
		"summary":     ev.Summary,
		"description": ev.Description,
	}
	tz := ev.TimeZone
	if tz == "" {
		tz = "UTC"
	}
	body["start"] = map[string]any{"dateTime": ev.Start, "timeZone": tz}
	body["end"] = map[string]any{"dateTime": ev.End, "timeZone": tz}
	if len(ev.Attendees) > 0 {
		atts := make([]map[string]string, 0, len(ev.Attendees))
		for _, email := range ev.Attendees {
			email = strings.TrimSpace(email)
			if email != "" {
				atts = append(atts, map[string]string{"email": email})
			}
		}
		body["attendees"] = atts
	}
	u := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events", url.PathEscape(calendarID))
	data, code, err := c.doJSON(ctx, http.MethodPost, u, body)
	if err != nil {
		return "", err
	}
	if err := checkStatus(code, data, "calendar create"); err != nil {
		return "", err
	}
	var parsed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &parsed)
	return parsed.ID, nil
}

// ListEvents returns upcoming events (maxResults).
func (c *Client) ListEvents(ctx context.Context, calendarID string, maxResults int) ([]map[string]any, error) {
	if calendarID == "" {
		calendarID = "primary"
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	u := fmt.Sprintf(
		"https://www.googleapis.com/calendar/v3/calendars/%s/events?maxResults=%d&singleEvents=true&orderBy=startTime",
		url.PathEscape(calendarID), maxResults,
	)
	data, code, err := c.doJSON(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(code, data, "calendar list"); err != nil {
		return nil, err
	}
	var parsed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.Items, nil
}

// GetEvent fetches one event by id.
func (c *Client) GetEvent(ctx context.Context, calendarID, eventID string) (map[string]any, error) {
	if calendarID == "" {
		calendarID = "primary"
	}
	if eventID == "" {
		return nil, fmt.Errorf("event_id required")
	}
	u := fmt.Sprintf(
		"https://www.googleapis.com/calendar/v3/calendars/%s/events/%s",
		url.PathEscape(calendarID), url.PathEscape(eventID),
	)
	data, code, err := c.doJSON(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(code, data, "calendar get"); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteEvent removes an event.
func (c *Client) DeleteEvent(ctx context.Context, calendarID, eventID string) error {
	if calendarID == "" {
		calendarID = "primary"
	}
	if eventID == "" {
		return fmt.Errorf("event_id required")
	}
	u := fmt.Sprintf(
		"https://www.googleapis.com/calendar/v3/calendars/%s/events/%s",
		url.PathEscape(calendarID), url.PathEscape(eventID),
	)
	data, code, err := c.doJSON(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	return checkStatus(code, data, "calendar delete")
}

// --- Drive ---

// ListFiles lists files, optionally under a parent folder.
func (c *Client) ListFiles(ctx context.Context, parentID string, pageSize int) ([]map[string]any, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	q := "trashed=false"
	if parentID != "" {
		q = fmt.Sprintf("'%s' in parents and trashed=false", strings.ReplaceAll(parentID, "'", "\\'"))
	}
	u := fmt.Sprintf(
		"https://www.googleapis.com/drive/v3/files?pageSize=%d&fields=files(id,name,mimeType,parents)&q=%s",
		pageSize, url.QueryEscape(q),
	)
	data, code, err := c.doJSON(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(code, data, "drive list"); err != nil {
		return nil, err
	}
	var parsed struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.Files, nil
}

// CreateFolder creates a Drive folder; optional parentID.
func (c *Client) CreateFolder(ctx context.Context, name, parentID string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("folder name required")
	}
	meta := map[string]any{
		"name":     name,
		"mimeType": "application/vnd.google-apps.folder",
	}
	if parentID != "" {
		meta["parents"] = []string{parentID}
	}
	data, code, err := c.doJSON(ctx, http.MethodPost, "https://www.googleapis.com/drive/v3/files", meta)
	if err != nil {
		return "", err
	}
	if err := checkStatus(code, data, "drive create folder"); err != nil {
		return "", err
	}
	var parsed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &parsed)
	return parsed.ID, nil
}

// UploadTextFile creates a plain-text file with the given content.
func (c *Client) UploadTextFile(ctx context.Context, name, content, parentID string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("file name required")
	}
	meta := map[string]any{"name": name, "mimeType": "text/plain"}
	if parentID != "" {
		meta["parents"] = []string{parentID}
	}
	metaJSON, _ := json.Marshal(meta)

	boundary := "whatomate_boundary"
	var buf bytes.Buffer
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: application/json; charset=UTF-8\r\n\r\n")
	buf.Write(metaJSON)
	buf.WriteString("\r\n--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/plain\r\n\r\n")
	buf.WriteString(content)
	buf.WriteString("\r\n--" + boundary + "--")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if err := checkStatus(resp.StatusCode, data, "drive upload"); err != nil {
		return "", err
	}
	var parsed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &parsed)
	return parsed.ID, nil
}

// --- Docs ---

// CreateDoc creates a Google Doc and returns its document id.
func (c *Client) CreateDoc(ctx context.Context, title string) (string, error) {
	if title == "" {
		title = "Untitled"
	}
	data, code, err := c.doJSON(ctx, http.MethodPost, "https://docs.googleapis.com/v1/documents", map[string]any{
		"title": title,
	})
	if err != nil {
		return "", err
	}
	if err := checkStatus(code, data, "docs create"); err != nil {
		return "", err
	}
	var parsed struct {
		DocumentID string `json:"documentId"`
	}
	_ = json.Unmarshal(data, &parsed)
	return parsed.DocumentID, nil
}

// AppendDocText inserts text at the end of the document body.
func (c *Client) AppendDocText(ctx context.Context, documentID, text string) error {
	if documentID == "" {
		return fmt.Errorf("document_id required")
	}
	// End-of-segment insert index: fetch doc length via a read, or use endOfSegmentLocation.
	body := map[string]any{
		"requests": []any{
			map[string]any{
				"insertText": map[string]any{
					"text": text,
					"endOfSegmentLocation": map[string]any{
						"segmentId": "",
					},
				},
			},
		},
	}
	u := fmt.Sprintf("https://docs.googleapis.com/v1/documents/%s:batchUpdate", url.PathEscape(documentID))
	data, code, err := c.doJSON(ctx, http.MethodPost, u, body)
	if err != nil {
		return err
	}
	return checkStatus(code, data, "docs append")
}

// ReadDoc returns plain text extracted from the document body.
func (c *Client) ReadDoc(ctx context.Context, documentID string) (string, error) {
	if documentID == "" {
		return "", fmt.Errorf("document_id required")
	}
	u := fmt.Sprintf("https://docs.googleapis.com/v1/documents/%s", url.PathEscape(documentID))
	data, code, err := c.doJSON(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if err := checkStatus(code, data, "docs read"); err != nil {
		return "", err
	}
	var doc struct {
		Body struct {
			Content []struct {
				Paragraph *struct {
					Elements []struct {
						TextRun *struct {
							Content string `json:"content"`
						} `json:"textRun"`
					} `json:"elements"`
				} `json:"paragraph"`
			} `json:"content"`
		} `json:"body"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, block := range doc.Body.Content {
		if block.Paragraph == nil {
			continue
		}
		for _, el := range block.Paragraph.Elements {
			if el.TextRun != nil {
				b.WriteString(el.TextRun.Content)
			}
		}
	}
	return b.String(), nil
}
