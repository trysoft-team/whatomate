package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/calling"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"github.com/zerodha/logf"
)

func pipecatTestLog() logf.Logger {
	return logf.New(logf.Opts{Level: logf.ErrorLevel})
}

func TestPipecatEvent_Unauthorized(t *testing.T) {
	app := &App{Config: &config.Config{Pipecat: config.PipecatConfig{AuthToken: "secret"}}}
	req := &fasthttp.RequestCtx{}
	req.Request.Header.SetMethod("POST")
	req.Request.SetBody([]byte(`{"call_id":"c1","type":"hangup"}`))
	r := &fastglue.Request{RequestCtx: req}
	err := app.PipecatEvent(r)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, req.Response.StatusCode())
}

func TestPipecatEvent_NotFound(t *testing.T) {
	m := calling.NewManager(&config.CallingConfig{}, nil, nil, nil, nil, nil, nil, pipecatTestLog())
	app := &App{
		Config:      &config.Config{Pipecat: config.PipecatConfig{AuthToken: "secret"}},
		CallManager: m,
	}
	req := &fasthttp.RequestCtx{}
	req.Request.Header.SetMethod("POST")
	req.Request.Header.Set("Authorization", "Bearer secret")
	req.Request.SetBody([]byte(`{"call_id":"missing","type":"hangup"}`))
	r := &fastglue.Request{RequestCtx: req}
	err := app.PipecatEvent(r)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, req.Response.StatusCode())
}

func TestPipecatTranscript_Unauthorized(t *testing.T) {
	app := &App{Config: &config.Config{Pipecat: config.PipecatConfig{AuthToken: "secret"}}}
	req := &fasthttp.RequestCtx{}
	req.Request.Header.SetMethod("POST")
	req.Request.SetBody([]byte(`{"lines":[{"role":"user","text":"hi"}]}`))
	r := &fastglue.Request{RequestCtx: req}
	require.NoError(t, app.PipecatTranscript(r))
	assert.Equal(t, http.StatusUnauthorized, req.Response.StatusCode())
}

func TestPipecatRecording_Unauthorized(t *testing.T) {
	app := &App{Config: &config.Config{Pipecat: config.PipecatConfig{AuthToken: "secret"}}}
	req := &fasthttp.RequestCtx{}
	req.Request.Header.SetMethod("POST")
	req.Request.SetBody([]byte("RIFF"))
	req.Request.Header.SetContentType("audio/wav")
	r := &fastglue.Request{RequestCtx: req}
	require.NoError(t, app.PipecatRecording(r))
	assert.Equal(t, http.StatusUnauthorized, req.Response.StatusCode())
}

func TestPipecatStatus(t *testing.T) {
	m := calling.NewManager(&config.CallingConfig{}, nil, nil, nil, nil, nil, nil, pipecatTestLog())
	m.SetPipecat(&config.PipecatConfig{Enabled: true, BaseURL: "http://x", AuthToken: "t"})
	app := &App{CallManager: m}
	req := &fasthttp.RequestCtx{}
	req.Request.Header.SetMethod("GET")
	r := &fastglue.Request{RequestCtx: req}
	require.NoError(t, app.PipecatStatus(r))
	assert.Equal(t, http.StatusOK, req.Response.StatusCode())
	body := string(req.Response.Body())
	assert.Contains(t, body, "enabled")
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(req.Response.Body(), &envelope))
}
