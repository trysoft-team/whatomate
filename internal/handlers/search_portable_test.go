package handlers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func TestSearch_CaseInsensitive(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	createTestCannedResponse(t, app, org.ID, user.ID, "Greeting Hello", "/greet", "x", "general")

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "search", "greeting hello")

	require.NoError(t, app.ListCannedResponses(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), "Greeting Hello")
}

// SQLite's LIKE folds ASCII letters only; documented limitation.
func TestSearch_NonASCIIFolding(t *testing.T) {
	if testutil.Driver() != "sqlite" {
		t.Skip("postgres and mysql fold non-ASCII")
	}
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	createTestCannedResponse(t, app, org.ID, user.ID, "Émile", "/emile", "x", "general")

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "search", "émile")

	require.NoError(t, app.ListCannedResponses(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.NotContains(t, string(testutil.GetResponseBody(req)), "Émile")
}
