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

func callGenerateTemplate(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) (int, []byte) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.GenerateTemplate(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

func TestGenerateTemplate(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"name\":\"order_shipped\",\"display_name\":\"Order shipped\",\"category\":\"UTILITY\",\"language\":\"en\",\"header_type\":\"NONE\",\"body_content\":\"Hi {{1}}, your order has shipped.\",\"body_examples\":[\"Maria\"]}"}]}}],"usageMetadata":{"promptTokenCount":700,"candidatesTokenCount":50,"totalTokenCount":750}}`))
	}))
	defer srv.Close()
	defer handlers.SetGoogleGenerateURLForTest(srv.URL + "/models/%s:generateContent")()

	app := newTestApp(t, withHTTPClient(srv.Client()))
	org := testutil.CreateTestOrganization(t, app.DB)
	writer := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Template Admin", false, false,
		chatAIPermissions(t, app, models.ResourceTemplates))
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("tpl-gen")), testutil.WithPassword("password"), testutil.WithRoleID(&writer.ID))
	reader := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Chat only", false, false,
		chatAIPermissions(t, app, models.ResourceChat))
	agent := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("tpl-gen-agent")), testutil.WithPassword("password"), testutil.WithRoleID(&reader.ID))

	code, _ := callGenerateTemplate(t, app, org.ID, agent.ID, map[string]any{"prompt": "shipping update"})
	assert.Equal(t, fasthttp.StatusForbidden, code)

	code, body := callGenerateTemplate(t, app, org.ID, user.ID, map[string]any{"prompt": "shipping update"})
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.Contains(t, string(body), "AI is not configured")

	enableTestAI(t, app, org.ID)
	require.NoError(t, app.DB.Create(&models.Template{
		OrganizationID: org.ID, WhatsAppAccount: "main", Name: "order_shipped", Language: "en", Category: "UTILITY", BodyContent: "x",
	}).Error)

	code, _ = callGenerateTemplate(t, app, org.ID, user.ID, map[string]any{"prompt": " "})
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	code, body = callGenerateTemplate(t, app, org.ID, user.ID, map[string]any{"prompt": "Tell customers their order shipped", "whatsapp_account": "main"})
	require.Equal(t, fasthttp.StatusOK, code, string(body))
	var resp struct {
		Data struct {
			WhatsAppAccount string           `json:"whatsapp_account"`
			Name            string           `json:"name"`
			BodyContent     string           `json:"body_content"`
			SampleValues    []map[string]any `json:"sample_values"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "main", resp.Data.WhatsAppAccount)
	assert.Equal(t, "order_shipped_2", resp.Data.Name, "existing name is not reused")
	assert.Equal(t, "Hi {{1}}, your order has shipped.", resp.Data.BodyContent)
	require.Len(t, resp.Data.SampleValues, 1)
	assert.Equal(t, "Maria", resp.Data.SampleValues[0]["value"])
	assert.Contains(t, gotPrompt, "Existing template names (do not reuse): order_shipped")

	var saved int64
	app.DB.Model(&models.Template{}).Where("organization_id = ?", org.ID).Count(&saved)
	assert.Equal(t, int64(1), saved, "generate must not save the template")

	var usage models.AIUsageLog
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&usage).Error)
	assert.Equal(t, models.AIUsagePurposeTemplateGenerate, usage.Purpose)
}
