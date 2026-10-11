package handlers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func TestTags_RenameAndDeleteRewriteContacts(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	odd := `vip"1 50%`
	c1 := testutil.CreateTestContact(t, app.DB, org.ID)
	c2 := testutil.CreateTestContact(t, app.DB, org.ID)
	both := testutil.CreateTestContact(t, app.DB, org.ID)
	deleted := testutil.CreateTestContact(t, app.DB, org.ID)
	other := testutil.CreateTestContact(t, app.DB, otherOrg.ID)
	require.NoError(t, app.DB.Model(c1).Update("tags", models.JSONBArray{odd, "keep"}).Error)
	require.NoError(t, app.DB.Model(c2).Update("tags", models.JSONBArray{"keep"}).Error)
	require.NoError(t, app.DB.Model(both).Update("tags", models.JSONBArray{odd, "gold"}).Error)
	require.NoError(t, app.DB.Model(deleted).Update("tags", models.JSONBArray{odd}).Error)
	require.NoError(t, app.DB.Delete(deleted).Error)
	require.NoError(t, app.DB.Model(other).Update("tags", models.JSONBArray{odd}).Error)

	tagsOf := func(id any) models.JSONBArray {
		var c models.Contact
		require.NoError(t, app.DB.Unscoped().First(&c, "id = ?", id).Error)
		return c.Tags
	}

	require.NoError(t, app.RewriteContactTagsForTest(org.ID, odd, "gold"))
	assert.Equal(t, models.JSONBArray{"gold", "keep"}, tagsOf(c1.ID))
	assert.Equal(t, models.JSONBArray{"gold"}, tagsOf(both.ID), "rename de-duplicates")
	assert.Equal(t, models.JSONBArray{"gold"}, tagsOf(deleted.ID), "soft-deleted contacts are rewritten too")
	assert.Equal(t, models.JSONBArray{odd}, tagsOf(other.ID), "other orgs are untouched")

	require.NoError(t, app.RewriteContactTagsForTest(org.ID, "gold", ""))
	assert.Equal(t, models.JSONBArray{"keep"}, tagsOf(c1.ID))
	assert.Equal(t, models.JSONBArray{"keep"}, tagsOf(c2.ID))
	assert.Equal(t, models.JSONBArray{}, tagsOf(both.ID))
	assert.Equal(t, models.JSONBArray{}, tagsOf(deleted.ID))
	assert.Equal(t, models.JSONBArray{odd}, tagsOf(other.ID))
}

func TestContacts_FilterByTagWithSpecialChars(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	hit := testutil.CreateTestContact(t, app.DB, org.ID)
	quoted := testutil.CreateTestContact(t, app.DB, org.ID)
	miss := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(hit).Update("tags", models.JSONBArray{"50%"}).Error)
	require.NoError(t, app.DB.Model(quoted).Update("tags", models.JSONBArray{`vip"1`}).Error)
	require.NoError(t, app.DB.Model(miss).Update("tags", models.JSONBArray{"500"}).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "tags", `50%,vip"1`)

	require.NoError(t, app.ListContacts(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, hit.ID.String())
	assert.Contains(t, body, quoted.ID.String())
	assert.NotContains(t, body, miss.ID.String())
}
