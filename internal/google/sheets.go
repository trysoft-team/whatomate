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

	"golang.org/x/oauth2/google"
)

const sheetsScope = "https://www.googleapis.com/auth/spreadsheets"

// SheetsClient talks to the Sheets v4 REST API using a service-account JSON.
type SheetsClient struct {
	http *http.Client
}

// NewSheetsClient builds an authenticated client from service-account JSON bytes.
func NewSheetsClient(ctx context.Context, saJSON []byte) (*SheetsClient, error) {
	cfg, err := google.JWTConfigFromJSON(saJSON, sheetsScope)
	if err != nil {
		return nil, fmt.Errorf("parse service account: %w", err)
	}
	return &SheetsClient{http: cfg.Client(ctx)}, nil
}

// AppendRows appends values to sheetName (A1 range = sheet name).
func (c *SheetsClient) AppendRows(ctx context.Context, spreadsheetID, sheetName string, rows [][]any) error {
	if spreadsheetID == "" {
		return fmt.Errorf("spreadsheet_id required")
	}
	rangeA1 := sheetName
	if rangeA1 == "" {
		rangeA1 = "Sheet1"
	}
	payload, _ := json.Marshal(map[string]any{"values": rows})
	u := fmt.Sprintf(
		"https://sheets.googleapis.com/v4/spreadsheets/%s/values/%s:append?valueInputOption=USER_ENTERED&insertDataOption=INSERT_ROWS",
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeA1),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sheets append %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ReadRange returns values for an A1 range (e.g. "Sheet1!A1:D10").
func (c *SheetsClient) ReadRange(ctx context.Context, spreadsheetID, rangeA1 string) ([][]any, error) {
	if spreadsheetID == "" || rangeA1 == "" {
		return nil, fmt.Errorf("spreadsheet_id and range required")
	}
	u := fmt.Sprintf(
		"https://sheets.googleapis.com/v4/spreadsheets/%s/values/%s",
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeA1),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("sheets read %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Values [][]any `json:"values"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed.Values, nil
}

// UpdateRange writes values to an A1 range.
func (c *SheetsClient) UpdateRange(ctx context.Context, spreadsheetID, rangeA1 string, rows [][]any) error {
	if spreadsheetID == "" || rangeA1 == "" {
		return fmt.Errorf("spreadsheet_id and range required")
	}
	payload, _ := json.Marshal(map[string]any{"values": rows})
	u := fmt.Sprintf(
		"https://sheets.googleapis.com/v4/spreadsheets/%s/values/%s?valueInputOption=USER_ENTERED",
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeA1),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sheets update %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
