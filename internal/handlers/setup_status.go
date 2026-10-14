package handlers

import "github.com/zerodha/fastglue"

// SetupStatus answers the frontend's first-run check once whatomate is set up.
func (a *App) SetupStatus(r *fastglue.Request) error {
	return r.SendEnvelope(map[string]bool{"needed": false, "ready": true})
}
