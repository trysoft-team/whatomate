package handlers

import "github.com/google/uuid"

// ResolveMetaAppCredsForTest exposes resolveMetaAppCreds for unit tests.
func (a *App) ResolveMetaAppCredsForTest(orgID uuid.UUID) (appID, appSecret, configID string, err error) {
	return a.resolveMetaAppCreds(orgID)
}
