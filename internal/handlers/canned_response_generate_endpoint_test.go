package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func callGenerateCannedResponse(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, prompt string) (int, []byte) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"prompt": prompt})
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.GenerateCannedResponse(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

func TestGenerateCannedResponse(t *testing.T) {
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Contents) > 0 && len(body.Contents[0].Parts) > 0 {
			gotPrompt = body.Contents[0].Parts[0].Text
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"name\":\"Welcome\",\"shortcut\":\"hi\",\"category\":\"greeting\",\"content\":\"Hi {{contact_name}}! How can we help?\",\"buttons\":[{\"type\":\"reply\",\"title\":\"Pricing\"}]}"}]}}],"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":40,"totalTokenCount":540}}`))
	}))
	defer srv.Close()
	defer handlers.SetGoogleGenerateURLForTest(srv.URL + "/models/%s:generateContent")()

	app := newTestApp(t, withHTTPClient(srv.Client()))
	org := testutil.CreateTestOrganization(t, app.DB)
	writer := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Canned Admin", false, false,
		chatAIPermissions(t, app, models.ResourceCannedResponses))
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("cr-gen")), testutil.WithPassword("password"), testutil.WithRoleID(&writer.ID))
	reader := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Chat only", false, false,
		chatAIPermissions(t, app, models.ResourceChat))
	agent := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("cr-gen-agent")), testutil.WithPassword("password"), testutil.WithRoleID(&reader.ID))

	code, _ := callGenerateCannedResponse(t, app, org.ID, agent.ID, "welcome")
	assert.Equal(t, fasthttp.StatusForbidden, code)

	code, body := callGenerateCannedResponse(t, app, org.ID, user.ID, "welcome")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.Contains(t, string(body), "AI is not configured")

	enableTestAI(t, app, org.ID)
	require.NoError(t, app.DB.Create(&models.CannedResponse{
		OrganizationID: org.ID, Name: "Welcome", Shortcut: "hi", Content: "Hello", IsActive: true, CreatedByID: user.ID,
	}).Error)

	code, _ = callGenerateCannedResponse(t, app, org.ID, user.ID, "  ")
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	code, body = callGenerateCannedResponse(t, app, org.ID, user.ID, "Warm welcome for new customers")
	require.Equal(t, fasthttp.StatusOK, code, string(body))
	var resp struct {
		Data struct {
			Name     string                          `json:"name"`
			Shortcut string                          `json:"shortcut"`
			Category string                          `json:"category"`
			Content  string                          `json:"content"`
			Buttons  []handlers.CannedResponseButton `json:"buttons"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "Welcome (2)", resp.Data.Name, "existing name is not reused")
	assert.Equal(t, "hi2", resp.Data.Shortcut)
	assert.Equal(t, "greeting", resp.Data.Category)
	assert.Equal(t, "Hi {{contact_name}}! How can we help?", resp.Data.Content)
	require.Len(t, resp.Data.Buttons, 1)
	assert.Equal(t, "btn_1", resp.Data.Buttons[0].ID)

	assert.Contains(t, gotPrompt, "Warm welcome for new customers")
	assert.Contains(t, gotPrompt, "- Welcome (/hi)", "existing responses are listed for the AI")

	var saved int64
	app.DB.Model(&models.CannedResponse{}).Where("organization_id = ?", org.ID).Count(&saved)
	assert.Equal(t, int64(1), saved, "generate must not save the response")

	var usage models.AIUsageLog
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&usage).Error)
	assert.Equal(t, models.AIUsagePurposeCannedResponseGenerate, usage.Purpose)
}
