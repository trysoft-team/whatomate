package handlers

import (
	"net/http"

	"github.com/trysoft/whatomate/pkg/whatsapp"
)

// FlowMediaURLAllowedForTest exposes the CDN host guard for unit tests.
func FlowMediaURLAllowedForTest(raw string) error {
	return flowMediaURLAllowed(raw)
}

// UseFlowMediaServerForTest lets Flow media downloads reach a local test
// server. Call the returned func to restore the production guard and client.
func UseFlowMediaServerForTest(client *http.Client) (restore func()) {
	prevAllowed, prevClient := flowMediaURLAllowed, flowMediaClient
	flowMediaURLAllowed = func(string) error { return nil }
	flowMediaClient = func() *whatsapp.Client { return &whatsapp.Client{HTTPClient: client} }
	return func() { flowMediaURLAllowed, flowMediaClient = prevAllowed, prevClient }
}

// BuildRoutingModelForTest exposes buildRoutingModel for unit tests.
func BuildRoutingModelForTest(screens []any) map[string]any {
	return buildRoutingModel(screens)
}

// SanitizeScreensForMetaOptsForTest exposes sanitizeScreensForMetaOpts for unit tests.
func SanitizeScreensForMetaOptsForTest(screens []any, endpoint bool) []any {
	return sanitizeScreensForMetaOpts(screens, endpoint)
}

// ValidateFlowStructureForTest exposes validateFlowStructure for unit tests.
func ValidateFlowStructureForTest(screens []any, endpoint bool) error {
	return validateFlowStructure(screens, endpoint)
}
