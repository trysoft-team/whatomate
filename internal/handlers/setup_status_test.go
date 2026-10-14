package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestSetupStatus_DoneInNormalMode(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	req := testutil.NewGETRequest(t)
	require.NoError(t, app.SetupStatus(req))
	var resp struct {
		Data struct {
			Needed bool `json:"needed"`
			Ready  bool `json:"ready"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.False(t, resp.Data.Needed)
	assert.True(t, resp.Data.Ready)
}
