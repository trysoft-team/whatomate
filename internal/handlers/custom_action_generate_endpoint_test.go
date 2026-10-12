package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func callGenerateCustomAction(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, prompt string) (int, []byte) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"prompt": prompt})
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.GenerateCustomAction(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

func TestGenerateCustomAction(t *testing.T) {
	reply := `{"candidates":[{"content":{"parts":[{"text":"{\"name\":\"Search phone\",\"icon\":\"globe\",\"action_type\":\"url\",\"config\":{\"url\":\"https://www.google.com/search?q={{contact.phone_number}}\"},\"notes\":\"\"}"}]}}],"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":60,"totalTokenCount":960}}`
	status := http.StatusOK
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
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(reply))
		} else {
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota"}}`))
		}
	}))
	defer srv.Close()
	defer handlers.SetGoogleGenerateURLForTest(srv.URL + "/models/%s:generateContent")()

	app := newTestApp(t, withHTTPClient(srv.Client()))
	org := testutil.CreateTestOrganization(t, app.DB)
	writer := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Actions Admin", false, false,
		chatAIPermissions(t, app, models.ResourceCustomActions))
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("ca-gen")), testutil.WithPassword("password"), testutil.WithRoleID(&writer.ID))
	reader := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Chat only", false, false,
		chatAIPermissions(t, app, models.ResourceChat))
	agent := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("ca-gen-agent")), testutil.WithPassword("password"), testutil.WithRoleID(&reader.ID))

	code, _ := callGenerateCustomAction(t, app, org.ID, agent.ID, "open google")
	assert.Equal(t, fasthttp.StatusForbidden, code)

	code, body := callGenerateCustomAction(t, app, org.ID, user.ID, "open google")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.Contains(t, string(body), "AI is not configured")

	enableTestAI(t, app, org.ID)

	code, _ = callGenerateCustomAction(t, app, org.ID, user.ID, "   ")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	code, _ = callGenerateCustomAction(t, app, org.ID, user.ID, strings.Repeat("x", 4001))
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	code, body = callGenerateCustomAction(t, app, org.ID, user.ID, "Search the contact's phone on Google")
	require.Equal(t, fasthttp.StatusOK, code, string(body))
	var resp struct {
		Data struct {
			Name       string         `json:"name"`
			Icon       string         `json:"icon"`
			ActionType string         `json:"action_type"`
			Config     map[string]any `json:"config"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "Search phone", resp.Data.Name)
	assert.Equal(t, "url", resp.Data.ActionType)
	assert.Equal(t, "https://www.google.com/search?q={{contact.phone_number}}", resp.Data.Config["url"])
	assert.Contains(t, gotPrompt, "Search the contact's phone on Google")

	var saved int64
	app.DB.Model(&models.CustomAction{}).Where("organization_id = ?", org.ID).Count(&saved)
	assert.Zero(t, saved, "generate must not save the action")

	var usage models.AIUsageLog
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&usage).Error)
	assert.Equal(t, models.AIUsagePurposeCustomActionGenerate, usage.Purpose)

	reply = `{"candidates":[{"content":{"parts":[{"text":"I cannot help with that."}]}}]}`
	code, _ = callGenerateCustomAction(t, app, org.ID, user.ID, "anything")
	assert.Equal(t, fasthttp.StatusBadGateway, code)

	status = http.StatusTooManyRequests
	code, body = callGenerateCustomAction(t, app, org.ID, user.ID, "anything")
	assert.Equal(t, fasthttp.StatusTooManyRequests, code)
	assert.Contains(t, string(body), "AI usage limit reached")
}
