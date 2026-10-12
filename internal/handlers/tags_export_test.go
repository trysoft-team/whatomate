package handlers

import "github.com/google/uuid"

// RewriteContactTagsForTest exposes rewriteContactTags for unit tests.
func (a *App) RewriteContactTagsForTest(orgID uuid.UUID, from, to string) error {
	return a.rewriteContactTags(orgID, from, to)
}
