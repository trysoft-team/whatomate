package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestApplyAgentReplyInitials_NotDuplicatedOnRetry(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(user).Update("full_name", "System Admin").Error)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		AgentAssignment: models.AgentAssignmentConfig{AppendAgentInitials: true},
	}).Error)

	req := &OutgoingMessageRequest{Account: account, Type: models.MessageTypeText, Content: "hi"}
	app.applyAgentReplyInitials(req, user.ID)
	assert.Equal(t, "hi\n\n`SA`", req.Content)

	retry := &OutgoingMessageRequest{Account: account, Type: models.MessageTypeText, Content: req.Content}
	app.applyAgentReplyInitials(retry, user.ID)
	assert.Equal(t, "hi\n\n`SA`", retry.Content, "retrying a sent reply must not add the initials again")

	caption := &OutgoingMessageRequest{Account: account, Type: models.MessageTypeImage, Caption: "`SA`"}
	app.applyAgentReplyInitials(caption, user.ID)
	assert.Equal(t, "`SA`", caption.Caption)
}
