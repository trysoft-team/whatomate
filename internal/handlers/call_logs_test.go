package handlers_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func TestGetCallRecording_LocalURL(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "logs-reader", []string{"call_logs:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	app.Config.Server.BasePath = ""

	key := "recordings/" + org.ID.String() + "/call.ogg"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(key)), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(key)), []byte("OggS-test"), 0644))

	callLog := &models.CallLog{
		OrganizationID:    org.ID,
		WhatsAppAccount:   "Main",
		ContactID:         contact.ID,
		CallerPhone:       "+15551234567",
		Direction:         models.CallDirectionIncoming,
		Status:            models.CallStatusCompleted,
		RecordingS3Key:    key,
		RecordingDuration: 12,
		AgentID:           &user.ID,
	}
	require.NoError(t, app.DB.Create(callLog).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", callLog.ID.String())

	require.NoError(t, app.GetCallRecording(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var data struct {
		URL      string `json:"url"`
		Duration int    `json:"duration"`
	}
	testutil.ParseEnvelopeResponse(t, req, &data)
	assert.Equal(t, "/api/call-logs/"+callLog.ID.String()+"/recording/file", data.URL)
	assert.Equal(t, 12, data.Duration)
}

func TestServeCallRecordingFile_Success(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "logs-reader", []string{"call_logs:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir

	key := "recordings/" + org.ID.String() + "/call.ogg"
	payload := []byte("OggS-local-recording")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(key)), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(key)), payload, 0644))

	callLog := &models.CallLog{
		OrganizationID:  org.ID,
		WhatsAppAccount: "Main",
		ContactID:       contact.ID,
		CallerPhone:     "+15551234567",
		Direction:       models.CallDirectionIncoming,
		Status:          models.CallStatusCompleted,
		RecordingS3Key:  key,
		AgentID:         &user.ID,
	}
	require.NoError(t, app.DB.Create(callLog).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", callLog.ID.String())

	require.NoError(t, app.ServeCallRecordingFile(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, "audio/ogg", string(req.RequestCtx.Response.Header.Peek("Content-Type")))
	assert.Equal(t, payload, testutil.GetResponseBody(req))
}

func TestServeCallRecordingFile_AgentOwnCallOnly(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	callLog := &models.CallLog{
		OrganizationID:  org.ID,
		WhatsAppAccount: "Main",
		ContactID:       contact.ID,
		CallerPhone:     "+15551234567",
		Direction:       models.CallDirectionIncoming,
		Status:          models.CallStatusCompleted,
		RecordingS3Key:  "recordings/x.ogg",
		AgentID:         &other.ID,
	}
	require.NoError(t, app.DB.Create(callLog).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetPathParam(req, "id", callLog.ID.String())

	require.NoError(t, app.ServeCallRecordingFile(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusNotFound, "Call log not found")
}

func TestGetCallRecording_NoRecording(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "logs-reader", []string{"call_logs:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	callLog := &models.CallLog{
		OrganizationID:  org.ID,
		WhatsAppAccount: "Main",
		ContactID:       contact.ID,
		CallerPhone:     "+15551234567",
		Direction:       models.CallDirectionIncoming,
		Status:          models.CallStatusCompleted,
	}
	require.NoError(t, app.DB.Create(callLog).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", callLog.ID.String())

	require.NoError(t, app.GetCallRecording(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusNotFound, "No recording for this call")
}

func TestGetCallRecording_BasePathPrefix(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "logs-reader", []string{"call_logs:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	app.Config.Storage.LocalPath = t.TempDir()
	app.Config.Server.BasePath = "/whatomate"

	callLog := &models.CallLog{
		OrganizationID:  org.ID,
		WhatsAppAccount: "Main",
		ContactID:       contact.ID,
		CallerPhone:     "+15551234567",
		Direction:       models.CallDirectionIncoming,
		Status:          models.CallStatusCompleted,
		RecordingS3Key:  "recordings/call.ogg",
		AgentID:         &user.ID,
	}
	require.NoError(t, app.DB.Create(callLog).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", callLog.ID.String())

	require.NoError(t, app.GetCallRecording(req))
	var data struct {
		URL string `json:"url"`
	}
	testutil.ParseEnvelopeResponse(t, req, &data)
	assert.Equal(t, "/whatomate/api/call-logs/"+callLog.ID.String()+"/recording/file", data.URL)
}
