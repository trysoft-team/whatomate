package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/logf"
)

type started struct {
	// url is the wizard's address without the token; opened is the URL
	// passed to OpenBrowser.
	url, opened, token string
	log                *syncBuffer
	res                chan Result
	err                chan error
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func startRun(t *testing.T, ctx context.Context, dir string) started {
	t.Helper()
	urls := make(chan string, 1)
	st := started{log: &syncBuffer{}, res: make(chan Result, 1), err: make(chan error, 1)}
	userDir := filepath.Join(dir, "user")
	go func() {
		res, err := Run(ctx, Options{
			Version: "test", Port: 0, Log: logf.New(logf.Opts{Writer: st.log}),
			Target:      config.SetupTarget{User: []config.Source{{Path: filepath.Join(userDir, "config.toml"), BaseDir: userDir}}},
			OpenBrowser: func(u string) error { urls <- u; return nil },
			MovedGrace:  3 * time.Second,
		})
		st.res <- res
		st.err <- err
	}()
	select {
	case st.opened = <-urls:
	case <-time.After(5 * time.Second):
		t.Fatal("setup did not start")
	}
	var ok bool
	st.url, st.token, ok = strings.Cut(st.opened, "/#token=")
	require.True(t, ok, "the opened URL carries the token in its fragment")
	return st
}

func (st started) postJSON(t *testing.T, path string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest("POST", st.url+path, bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, st.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func runBody(dir, url string, port int) map[string]any {
	return map[string]any{
		"config_path":  filepath.Join(dir, "user", "config.toml"),
		"port":         port,
		"database":     map[string]any{"driver": "sqlite", "path": filepath.Join(dir, "user", "whatomate.db")},
		"organization": "Acme",
		"admin":        map[string]any{"full_name": "Ana", "email": "ana@example.com", "password": "password-1"},
	}
}

func TestRun_SamePortHandsOverListener(t *testing.T) {
	dir := t.TempDir()
	st := startRun(t, context.Background(), dir)
	var port int
	_, err := fmt.Sscanf(st.url, "http://localhost:%d", &port)
	require.NoError(t, err)

	resp := st.postJSON(t, "/api/setup/complete", runBody(dir, st.url, port))
	require.Equal(t, 200, resp.StatusCode)
	_ = resp.Body.Close()

	res := <-st.res
	require.NoError(t, <-st.err)
	require.NotNil(t, res.Listener)
	assert.Equal(t, port, res.Listener.Addr().(*net.TCPAddr).Port)

	app := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("app") }}
	go func() { _ = app.Serve(res.Listener) }()
	defer func() { _ = app.Shutdown() }()
	get, err := http.Get(st.url + "/anything")
	require.NoError(t, err)
	b, _ := io.ReadAll(get.Body)
	_ = get.Body.Close()
	assert.Equal(t, "app", string(b), "the app serves on the setup socket")
}

func TestRun_WizardURLCarriesToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	st := startRun(t, ctx, dir)
	assert.Regexp(t, `^http://localhost:\d+$`, st.url)
	assert.Regexp(t, `^[A-Za-z0-9_-]{32}$`, st.token, "192 random bits, URL-safe")
	assert.Contains(t, st.log.String(), st.opened, "the logged URL carries the token")

	get, err := http.Get(st.url + "/api/setup/backups?config_path=" + filepath.Join(dir, "user", "config.toml"))
	require.NoError(t, err)
	_ = get.Body.Close()
	assert.Equal(t, 401, get.StatusCode)
	resp := st.postJSON(t, "/api/setup/database/test", map[string]any{"driver": "sqlite", "path": filepath.Join(dir, "user", "w.db")})
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	other := startRun(t, ctx, t.TempDir())
	assert.NotEqual(t, st.token, other.token, "a new token every run")

	resp = st.postJSON(t, "/api/setup/complete", runBody(dir, st.url, freePort(t)))
	_ = resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	require.NoError(t, <-st.err)
	for _, line := range strings.Split(st.log.String(), "\n") {
		if strings.Contains(line, st.token) {
			assert.Contains(t, line, "setup wizard", "the token appears only in the wizard URL lines")
		}
	}
}

func TestRun_PortChangeLeavesRedirect(t *testing.T) {
	dir := t.TempDir()
	st := startRun(t, context.Background(), dir)
	newPort := freePort(t)

	resp := st.postJSON(t, "/api/setup/complete", runBody(dir, st.url, newPort))
	require.Equal(t, 200, resp.StatusCode)
	_ = resp.Body.Close()

	res := <-st.res
	require.NoError(t, <-st.err)
	assert.Nil(t, res.Listener, "the app binds the new port itself")

	get, err := http.Get(st.url + "/api/setup/status")
	require.NoError(t, err)
	var out struct {
		Data statusResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(get.Body).Decode(&out))
	_ = get.Body.Close()
	assert.True(t, out.Data.Ready)
	assert.Equal(t, fmt.Sprintf("http://localhost:%d", newPort), out.Data.URL)
}

func TestListen_SkipsPortTakenOnLoopback(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = busy.Close() }()
	taken := busy.Addr().(*net.TCPAddr).Port

	ln, err := listen(taken)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	assert.NotEqual(t, taken, ln.Addr().(*net.TCPAddr).Port, "another program's loopback listener keeps the port")
}

func TestRun_AcceptsLargeUploadChunk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	st := startRun(t, ctx, dir)
	configPath := filepath.Join(dir, "user", "config.toml")
	body := map[string]any{"config_path": configPath, "name": "big.zip", "size": 8 << 20}
	resp := st.postJSON(t, "/api/setup/backup-uploads", body)
	require.Equal(t, 200, resp.StatusCode)
	var out struct{ Data struct{ ID string } }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	_ = resp.Body.Close()

	req, err := http.NewRequest("PUT", st.url+"/api/setup/backup-uploads/"+out.Data.ID+"?offset=0", bytes.NewReader(make([]byte, 8<<20)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(tokenHeader, st.token)
	put, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = put.Body.Close()
	assert.Equal(t, 200, put.StatusCode)
}

func TestRun_CancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := startRun(t, ctx, t.TempDir())
	cancel()
	assert.ErrorIs(t, <-st.err, context.Canceled)
	_, err := http.Get(st.url + "/api/setup/status")
	assert.Error(t, err, "the socket is closed")
}
