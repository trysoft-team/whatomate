package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func createChatMessage(t *testing.T, app *handlers.App, orgID uuid.UUID, contact *models.Contact, dir models.Direction, status models.MessageStatus) {
	t.Helper()
	require.NoError(t, app.DB.Create(&models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  orgID,
		ContactID:       contact.ID,
		WhatsAppAccount: "acct",
		Direction:       dir,
		MessageType:     models.MessageTypeText,
		Content:         "hi",
		Status:          status,
	}).Error)
}

type unreadCountResponse struct {
	Data struct {
		Messages int64 `json:"messages"`
		Chats    int64 `json:"chats"`
	} `json:"data"`
}

func TestApp_UnreadCount_CountsUnreadIncomingMessages(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	c1 := testutil.CreateTestContact(t, app.DB, org.ID)
	c2 := testutil.CreateTestContact(t, app.DB, org.ID)
	c3 := testutil.CreateTestContact(t, app.DB, org.ID)

	createChatMessage(t, app, org.ID, c1, models.DirectionIncoming, models.MessageStatusDelivered)
	createChatMessage(t, app, org.ID, c1, models.DirectionIncoming, models.MessageStatusDelivered)
	createChatMessage(t, app, org.ID, c2, models.DirectionIncoming, models.MessageStatusDelivered)
	createChatMessage(t, app, org.ID, c3, models.DirectionIncoming, models.MessageStatusRead)
	createChatMessage(t, app, org.ID, c3, models.DirectionOutgoing, models.MessageStatusSent)

	other := testutil.CreateTestOrganization(t, app.DB)
	otherContact := testutil.CreateTestContact(t, app.DB, other.ID)
	createChatMessage(t, app, other.ID, otherContact, models.DirectionIncoming, models.MessageStatusDelivered)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.GetUnreadCount(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp unreadCountResponse
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, int64(3), resp.Data.Messages)
	assert.Equal(t, int64(2), resp.Data.Chats)
}

func TestApp_UnreadCount_AgentOnlyCountsAssignedContacts(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	chatOnly := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "chat-only", []string{"chat:read", "chat:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&chatOnly.ID))

	mine := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(mine).Update("assigned_user_id", agent.ID).Error)
	notMine := testutil.CreateTestContact(t, app.DB, org.ID)

	createChatMessage(t, app, org.ID, mine, models.DirectionIncoming, models.MessageStatusDelivered)
	createChatMessage(t, app, org.ID, notMine, models.DirectionIncoming, models.MessageStatusDelivered)
	createChatMessage(t, app, org.ID, notMine, models.DirectionIncoming, models.MessageStatusDelivered)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.GetUnreadCount(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp unreadCountResponse
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, int64(1), resp.Data.Messages)
	assert.Equal(t, int64(1), resp.Data.Chats)
}

func TestApp_ListContacts_FilterByWhatsAppAccount(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	sales := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount("sales"))
	testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount("support"))
	testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount("support"))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "whatsapp_account", "sales")
	require.NoError(t, app.ListContacts(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data struct {
			Contacts []handlers.ContactResponse `json:"contacts"`
			Total    int64                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, int64(1), resp.Data.Total)
	require.Len(t, resp.Data.Contacts, 1)
	assert.Equal(t, sales.ID, resp.Data.Contacts[0].ID)
}
