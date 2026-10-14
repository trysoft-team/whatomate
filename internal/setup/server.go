package setup

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/frontend"
	"github.com/trysoft/whatomate/internal/netutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// Options configures Run.
type Options struct {
	Version string
	Target  config.SetupTarget
	// Port is tried first; a free port is used when it is busy.
	Port int
	Log  logf.Logger
	// Checker defaults to NewChecker(Version).
	Checker *Checker
	// OpenBrowser defaults to the platform opener.
	OpenBrowser func(url string) error
	// MovedGrace is how long the setup port redirects after a port change
	// (default 2 minutes).
	MovedGrace time.Duration
	// SharedDataDir, when set and writable, holds uploads and call audio
	// instead of the config location's data folder. See DefaultSharedDataDir.
	SharedDataDir string
}

// Result is how normal startup continues after setup.
type Result struct {
	// Listener is the setup socket for the app to serve on when the chosen
	// port is the one setup used; nil when the app must bind a new port.
	Listener net.Listener
}

// Run serves the setup wizard until it completes or ctx ends.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.Checker == nil {
		o.Checker = NewChecker(o.Version)
	}
	if o.OpenBrowser == nil {
		o.OpenBrowser = openBrowser
	}
	if o.MovedGrace == 0 {
		o.MovedGrace = 2 * time.Minute
	}
	token, err := newSecret(24)
	if err != nil {
		return Result{}, err
	}
	raw, err := listen(o.Port)
	if err != nil {
		return Result{}, err
	}
	port := raw.Addr().(*net.TCPAddr).Port
	shared := Share(raw)
	s := newServer(o, port, token)
	srv := &fasthttp.Server{
		Handler: s.handle, Name: "Whatomate setup",
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		// Upload chunks are up to backup.ChunkMax; the default limit is 4 MB.
		MaxRequestBodySize: 16 << 20,
		HeaderReceived: func(h *fasthttp.RequestHeader) fasthttp.RequestConfig {
			if isUploadChunk(h.Method(), h.RequestURI()) {
				return fasthttp.RequestConfig{ReadTimeout: 5 * time.Minute}
			}
			return fasthttp.RequestConfig{}
		},
	}
	go func() { _ = srv.Serve(shared.View(false)) }()

	urls := listenURLs(port)
	o.Log.Info("No config file found. Open the setup wizard to finish installing", "url", wizardURL(urls[0], token))
	for _, u := range urls[1:] {
		o.Log.Info("The setup wizard is also reachable at", "url", wizardURL(u, token))
	}
	if err := o.OpenBrowser(wizardURL(urls[0], token)); err != nil {
		o.Log.Debug("Could not open a browser", "error", err)
	}

	select {
	case <-ctx.Done():
		_ = srv.Shutdown()
		_ = shared.Close()
		return Result{}, ctx.Err()
	case f := <-s.done:
		_ = srv.Shutdown()
		if f.port == port {
			return Result{Listener: shared.View(true)}, nil
		}
		serveMoved(shared.View(true), f.url, o.MovedGrace)
		return Result{}, nil
	}
}

func listen(port int) (net.Listener, error) {
	// PortFree also sees another program's listener on a single address,
	// which a wildcard bind can share on macOS/BSD.
	if netutil.PortFree(port) {
		if ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port)); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", ":0")
}

const tokenHeader = "X-Setup-Token"

type finished struct {
	port int
	url  string
}

type candidateInfo struct {
	Path     string `json:"path"`
	DataDir  string `json:"data_dir"`
	Scope    string `json:"scope"`
	Writable bool   `json:"writable"`
	base     string
}

type statusResponse struct {
	Needed          bool            `json:"needed"`
	Ready           bool            `json:"ready"`
	TokenRequired   bool            `json:"token_required,omitempty"`
	URL             string          `json:"url,omitempty"`
	Version         string          `json:"version,omitempty"`
	ListenPort      int             `json:"listen_port,omitempty"`
	ConfigPath      string          `json:"config_path,omitempty"`
	ConfigPathFixed bool            `json:"config_path_fixed,omitempty"`
	Candidates      []candidateInfo `json:"candidates,omitempty"`
}

type server struct {
	o          Options
	listenPort int
	// token is the one-time key every setup API call must send.
	token    string
	frontend fasthttp.RequestHandler
	done     chan finished

	completeMu sync.Mutex // one complete at a time
	stateMu    sync.Mutex
	finished   *finished
	// seeded is the admin an earlier, failed complete created and the
	// database it is in. Guarded by completeMu.
	seeded *seededAdmin

	uploads backup.Uploads

	// restore is the background restore's progress. Guarded by stateMu.
	restore restoreState
	// restoreSeen is signalled when the page reads a finished restore.
	restoreSeen chan struct{}
	// restoreAckWait is how long a finished restore waits for the page.
	restoreAckWait time.Duration

	// beforeWrite runs just before the config file is written; tests use it.
	beforeWrite func()
	// migrate prepares the database; tests replace it to fail part-way.
	migrate func(*gorm.DB, *config.DefaultAdminConfig) error
}

func newServer(o Options, listenPort int, token string) *server {
	fe := fasthttp.RequestHandler(notEmbedded)
	if frontend.IsEmbedded() {
		fe = frontend.Handler("")
	}
	return &server{
		o: o, listenPort: listenPort, token: token, frontend: fe, done: make(chan finished, 1),
		migrate:     database.RunMigrationWithProgress,
		restoreSeen: make(chan struct{}, 1), restoreAckWait: 5 * time.Second,
	}
}

func notEmbedded(ctx *fasthttp.RequestCtx) {
	ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
	ctx.SetContentType("text/plain; charset=utf-8")
	ctx.SetBodyString("This build has no embedded frontend. Run `npm run dev` in frontend/ with WHATOMATE_API_URL set to this server's address.")
}

func (s *server) handle(ctx *fasthttp.RequestCtx) {
	h := &ctx.Response.Header
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	path := string(ctx.Path())
	if strings.HasPrefix(path, "/api/setup/") && !s.authorized(ctx) {
		// The page needs status to tell the operator to open the full link.
		if path == "/api/setup/status" && ctx.IsGet() {
			s.status(ctx, false)
			return
		}
		writeError(ctx, fasthttp.StatusUnauthorized, "This page needs the one-time setup key. Open the full link printed in whatomate's log.")
		return
	}
	// Only JSON bodies: other types are CORS "simple requests" any website
	// can send from the operator's browser without a preflight.
	if ctx.IsPost() && !isJSON(ctx) {
		writeError(ctx, fasthttp.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	switch {
	case path == "/api/setup/status" && ctx.IsGet():
		s.status(ctx, true)
	case path == "/api/setup/checks" && ctx.IsGet():
		s.checks(ctx)
	case path == "/api/setup/database/test" && ctx.IsPost():
		s.databaseTest(ctx)
	case path == "/api/setup/database/create" && ctx.IsPost():
		s.databaseCreate(ctx)
	case path == "/api/setup/complete" && ctx.IsPost():
		s.complete(ctx)
	case path == "/api/setup/backups" && ctx.IsGet():
		s.listBackups(ctx)
	case path == uploadsPath && ctx.IsPost():
		s.startUpload(ctx)
	case strings.HasPrefix(path, uploadsPath+"/"):
		s.uploadItem(ctx, strings.TrimPrefix(path, uploadsPath+"/"))
	case path == "/api/setup/backup/inspect" && ctx.IsPost():
		s.inspectBackup(ctx)
	case path == "/api/setup/restore" && ctx.IsPost():
		s.startRestore(ctx)
	case path == "/api/setup/restore" && ctx.IsGet():
		s.restoreStatus(ctx)
	case strings.HasPrefix(path, "/api/") || path == "/ws":
		writeError(ctx, fasthttp.StatusNotFound, "whatomate is not set up yet")
	default:
		s.frontend(ctx)
	}
}

// authorized reports whether the request carries the setup token.
func (s *server) authorized(ctx *fasthttp.RequestCtx) bool {
	got := ctx.Request.Header.Peek(tokenHeader)
	return s.token != "" && subtle.ConstantTimeCompare(got, []byte(s.token)) == 1
}

// wizardURL puts the token in the fragment, which browsers never send, so it
// stays out of request logs, proxies and Referer headers.
func wizardURL(base, token string) string {
	return base + "/#token=" + token
}

func isJSON(ctx *fasthttp.RequestCtx) bool {
	mt, _, err := mime.ParseMediaType(string(ctx.Request.Header.ContentType()))
	return err == nil && mt == "application/json"
}

func writeData(ctx *fasthttp.RequestCtx, data any) {
	b, _ := json.Marshal(map[string]any{"status": "success", "data": data})
	ctx.SetContentType("application/json")
	ctx.SetBody(b)
}

func writeError(ctx *fasthttp.RequestCtx, code int, msg string) {
	b, _ := json.Marshal(map[string]any{"status": "error", "message": msg, "data": nil})
	ctx.SetStatusCode(code)
	ctx.SetContentType("application/json")
	ctx.SetBody(b)
}

func (s *server) isFinished() *finished {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.finished
}

func (s *server) candidates() []candidateInfo {
	if f := s.o.Target.Fixed; f != "" {
		dir := filepath.Dir(f)
		return []candidateInfo{{Path: f, DataDir: dir, Scope: "fixed", Writable: Writable(dir)}}
	}
	var out []candidateInfo
	add := func(scope string, srcs []config.Source) {
		for _, c := range srcs {
			out = append(out, candidateInfo{
				Path: c.Path, DataDir: c.BaseDir, Scope: scope, base: c.BaseDir,
				Writable: Writable(filepath.Dir(c.Path)) && Writable(c.BaseDir),
			})
		}
	}
	add("user", s.o.Target.User)
	add("system", s.o.Target.System)
	return out
}

// defaultChoice prefers a writable system location, then a writable user
// location, then the first user location.
func defaultChoice(cands []candidateInfo) string {
	for _, scope := range []string{"fixed", "system", "user"} {
		for _, c := range cands {
			if c.Scope == scope && (c.Writable || scope == "fixed") {
				return c.Path
			}
		}
	}
	if len(cands) > 0 {
		return cands[0].Path
	}
	return ""
}

func (s *server) lookup(path string) (candidateInfo, bool) {
	for _, c := range s.candidates() {
		if c.Path == path {
			return c, true
		}
	}
	return candidateInfo{}, false
}

// status answers without the token too, but then only with what the page
// needs to ask for the key or follow the hand-off.
func (s *server) status(ctx *fasthttp.RequestCtx, authorized bool) {
	if f := s.isFinished(); f != nil {
		writeData(ctx, statusResponse{URL: f.url})
		return
	}
	if !authorized {
		writeData(ctx, statusResponse{Needed: true, Version: s.o.Version, TokenRequired: true})
		return
	}
	cands := s.candidates()
	writeData(ctx, statusResponse{
		Needed: true, Version: s.o.Version, ListenPort: s.listenPort,
		ConfigPath: defaultChoice(cands), ConfigPathFixed: s.o.Target.Fixed != "", Candidates: cands,
	})
}

func (s *server) checks(ctx *fasthttp.RequestCtx) {
	args := ctx.QueryArgs()
	port, _ := strconv.Atoi(string(args.Peek("port")))
	c, ok := s.lookup(string(args.Peek("config_path")))
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	results := s.o.Checker.Run(context.Background(), CheckInput{
		Port: port, ListenPort: s.listenPort, ConfigDir: filepath.Dir(c.Path), DataDir: c.DataDir,
	})
	writeData(ctx, map[string]any{"checks": results})
}

type databaseResult struct {
	OK             bool   `json:"ok"`
	DatabaseExists bool   `json:"database_exists"`
	Error          string `json:"error,omitempty"`
}

func (s *server) databaseTest(ctx *fasthttp.RequestCtx) {
	var in databaseInput
	if err := json.Unmarshal(ctx.PostBody(), &in); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	c := in.config()
	if err := validateDatabase(c); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if c.Driver == config.DriverSQLite {
		dir := filepath.Dir(c.Path)
		if !Writable(dir) {
			writeData(ctx, databaseResult{Error: dir + " is not writable"})
			return
		}
		writeData(ctx, databaseResult{OK: true, DatabaseExists: fileExists(c.Path)})
		return
	}
	exists, err := database.Probe(&c)
	if err != nil {
		writeData(ctx, databaseResult{Error: err.Error()})
		return
	}
	writeData(ctx, databaseResult{OK: true, DatabaseExists: exists})
}

func (s *server) databaseCreate(ctx *fasthttp.RequestCtx) {
	var in databaseInput
	if err := json.Unmarshal(ctx.PostBody(), &in); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	c := in.config()
	if err := validateDatabase(c); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if c.Driver == config.DriverSQLite {
		writeError(ctx, fasthttp.StatusBadRequest, "SQLite databases are created automatically")
		return
	}
	if !database.ValidDatabaseName(c.Name) {
		writeError(ctx, fasthttp.StatusBadRequest, "database name: use 1-63 letters, digits or underscores")
		return
	}
	if err := database.CreateDatabase(&c); err != nil {
		writeData(ctx, databaseResult{Error: err.Error()})
		return
	}
	writeData(ctx, databaseResult{OK: true, DatabaseExists: true})
}

func (s *server) complete(ctx *fasthttp.RequestCtx) {
	s.completeMu.Lock()
	defer s.completeMu.Unlock()
	if s.isFinished() != nil {
		writeError(ctx, fasthttp.StatusConflict, "setup is already complete")
		return
	}
	if s.getRestore().busy() {
		writeError(ctx, fasthttp.StatusConflict, "a restore is running")
		return
	}
	var req completeRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	origin, https := browserOrigin(ctx, req.Port, s.listenPort)
	if code, err := s.finish(req, https); err != nil {
		writeError(ctx, code, err.Error())
		return
	}
	s.finishSetup(finished{port: req.Port, url: origin})
	writeData(ctx, map[string]string{"url": origin})
}
