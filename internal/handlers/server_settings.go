package handlers

import (
	"errors"
	"fmt"
	"os"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/netutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ServerSettingsResponse describes the install-wide server settings.
type ServerSettingsResponse struct {
	// Port is the port the app is running on; ConfigPort is the one in the
	// config file, which takes effect after a restart.
	Port       int    `json:"port"`
	ConfigPort int    `json:"config_port"`
	ConfigPath string `json:"config_path"`
	Writable   bool   `json:"writable"`
}

func (a *App) serverSettings() ServerSettingsResponse {
	resp := ServerSettingsResponse{Port: a.Config.Server.Port, ConfigPort: a.Config.Server.Port, ConfigPath: a.ConfigPath}
	if a.ConfigPath == "" {
		return resp
	}
	if p, err := config.ReadServerPort(a.ConfigPath); err == nil {
		resp.ConfigPort = p
	}
	if f, err := os.OpenFile(a.ConfigPath, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		resp.Writable = true
	}
	return resp
}

// GetServerSettings returns the server port and config file. Super admin only.
func (a *App) GetServerSettings(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	return r.SendEnvelope(a.serverSettings())
}

// UpdateServerSettings saves a new port to the config file; it applies after
// a restart. Super admin only.
func (a *App) UpdateServerSettings(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	var req struct {
		Port int `json:"port"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Port < 1 || req.Port > 65535 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Port must be between 1 and 65535", nil, "")
	}
	if a.ConfigPath == "" {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "No config file is loaded", nil, "")
	}
	if req.Port != a.Config.Server.Port && !netutil.PortFree(req.Port) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, fmt.Sprintf("Port %d is in use by another program", req.Port), nil, "")
	}
	if err := config.SetServerPort(a.ConfigPath, req.Port); err != nil {
		if errors.Is(err, config.ErrCannotEditPort) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				fmt.Sprintf("Can't change the port in %s automatically; set [server] port by hand and restart.", a.ConfigPath), nil, "")
		}
		a.Log.Error("Failed to save server port", "path", a.ConfigPath, "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, fmt.Sprintf("Could not save %s: %v", a.ConfigPath, err), nil, "")
	}
	return r.SendEnvelope(a.serverSettings())
}
