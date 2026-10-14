package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/trysoft/whatomate/internal/assignment"
	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/calling"
	"github.com/trysoft/whatomate/internal/calling/defaultaudio"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/frontend"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/maintenance"
	"github.com/trysoft/whatomate/internal/middleware"
	"github.com/trysoft/whatomate/internal/queue"
	"github.com/trysoft/whatomate/internal/setup"
	"github.com/trysoft/whatomate/internal/storage"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/tts"
	"github.com/trysoft/whatomate/internal/websocket"
	"github.com/trysoft/whatomate/internal/worker"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"server"}
	}

	switch args[0] {
	case "server":
		runServer(args[1:])
	case "worker":
		runWorker(args[1:])
	case "config":
		runConfig(args[1:])
	case "backup":
		runBackupCmd(args[1:])
	case "restore":
		runRestoreCmd(args[1:])
	case "version":
		fmt.Printf("Whatomate %s (built %s)\n", Version, BuildTime)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Whatomate - WhatsApp Business API Platform

Usage:
  whatomate [command] [options]

Commands:
  server    Start the API server (with optional embedded workers)
	worker    Start background workers only (no API server)
	config    "config path" shows which config file would be used
  backup    Back up the database (and files) into the backup folder
  restore   Restore a backup into the configured database
	version   Show version information
  help      Show this help message

With no command, whatomate runs "server". When no config file is found,
server opens a setup wizard in the browser (port 4747) and writes one.

Server Options:
  -config string    Path to config file (default: search, see below)
  -migrate          Run database migrations on startup
  -workers int      Number of embedded workers (0 to disable) (default 1)

Worker Options:
  -config string    Path to config file (default: search, see below)
  -workers int      Number of workers to run (default 1)

Backup Options:
  -config string    Path to config file
  -no-files         Leave out uploaded media and audio
  -o string         Write to this file instead of the backup folder

Restore Options:
  -config string    Path to config file
  -yes              Do not ask to type RESTORE

Config file search (first found wins):
  -config, then $WHATOMATE_CONFIG, then (the current folder is not searched)
  Linux:   ~/.config/whatomate/config.toml, /etc/whatomate/config.toml
  macOS:   ~/Library/Application Support/whatomate/config.toml,
           ~/.config/whatomate/config.toml,
           /opt/homebrew/etc/whatomate/config.toml, /usr/local/etc/whatomate/config.toml
  Windows: %AppData%\whatomate\config.toml, %ProgramData%\whatomate\config.toml

Examples:
  whatomate server                     # API + 1 embedded worker
  whatomate server -workers 0          # API only (no workers)
  whatomate server -workers 4          # API + 4 embedded workers
  whatomate server -migrate            # Run migrations and start server
  whatomate worker -workers 4          # 4 workers only (no API)

Deployment Scenarios:
  All-in-one:    whatomate server
  Separate:      whatomate server -workers 0  (on API server)
                 whatomate worker -workers 4  (on worker server)`)
}

// ============================================================================
// SERVER COMMAND
// ============================================================================

func runServer(args []string) {
	serverFlags := flag.NewFlagSet("server", flag.ExitOnError)
	configPath := serverFlags.String("config", "", "Path to config file (default: search standard locations)")
	migrate := serverFlags.Bool("migrate", false, "Run database migrations")
	numWorkers := serverFlags.Int("workers", 1, "Number of workers to run (0 to disable embedded workers)")
	_ = serverFlags.Parse(args)

	// Initialize logger
	lo := logf.New(logf.Opts{
		EnableColor:     true,
		Level:           logf.DebugLevel,
		EnableCaller:    true,
		TimestampFormat: "2006-01-02 15:04:05",
		DefaultFields:   []any{"app", "whatomate"},
	})

	lo.Info("Starting Whatomate server...", "version", Version)

	var handoff net.Listener
	if _, err := config.Locate(*configPath); errors.Is(err, config.ErrNotFound) {
		handoff = runSetup(*configPath, lo)
	}
	cfg, cfgPath := loadConfig(*configPath, lo)
	switch claimed, other, err := backup.ClaimServerLock(cfg.Backup.Dir); {
	case err != nil:
		lo.Warn("Could not write the server lock in the backup folder", "dir", cfg.Backup.Dir, "error", err)
		backup.CleanTemp(cfg.Backup.Dir)
	case !claimed:
		lo.Warn("Another whatomate process is using this backup folder; leaving its lock and temp files alone", "pid", other.PID)
	default:
		if other.FromOtherHost() {
			lo.Warn("Took over the backup folder lock written on another host; make sure no other whatomate server uses this folder", "host", other.Host, "pid", other.PID)
		}
		backup.CleanTemp(cfg.Backup.Dir)
		defer backup.RemoveServerLock(cfg.Backup.Dir)
		for _, dir := range backup.LeftoverWorkDirs(cliAreas(cfg)) {
			lo.Warn("Folder left by an interrupted restore; check it and delete it when it is no longer needed", "path", dir)
		}
	}
	gate := &maintenance.Gate{}

	// Validate JWT secret
	if cfg.App.Environment == "production" && len(cfg.JWT.Secret) < 32 {
		lo.Fatal("JWT secret must be at least 32 characters in production")
	}
	if cfg.JWT.Secret == "" {
		lo.Warn("JWT secret is empty, using a random secret (tokens will not persist across restarts)")
	}

	// Warn if debug mode is on in production
	if cfg.App.Environment == "production" && cfg.App.Debug {
		lo.Warn("Debug mode is enabled in production! This may expose sensitive information.")
	}

	// Require explicit CORS origins in production
	if cfg.App.Environment == "production" && cfg.Server.AllowedOrigins == "" {
		lo.Fatal("server.allowed_origins must be set in production (e.g. \"https://app.example.com\")")
	}

	// Set log level based on environment
	if cfg.App.Environment == "production" {
		lo = logf.New(logf.Opts{
			Level:           logf.InfoLevel,
			TimestampFormat: "2006-01-02 15:04:05",
			DefaultFields:   []any{"app", "whatomate"},
		})
	}

	// Connect to the database
	db, err := database.Open(&cfg.Database, cfg.App.Debug)
	if err != nil {
		lo.Fatal("Failed to connect to database", "error", err)
	}
	lo.Info("Connected to database", "driver", cfg.Database.Driver)

	// Run migrations if requested
	if *migrate {
		if err := database.RunMigrationWithProgress(db, &cfg.DefaultAdmin); err != nil {
			lo.Fatal("Migration failed", "error", err)
		}
		// Backfill v2 graph for any legacy chatbot flow still on Steps[].
		// Idempotent — re-running is a no-op once every row is converted.
		if err := handlers.BackfillChatbotFlowGraph(db, lo); err != nil {
			lo.Fatal("Chatbot flow graph backfill failed", "error", err)
		}
		if err := handlers.BackfillChatbotFlowDraftGraph(db, lo); err != nil {
			lo.Fatal("Chatbot flow draft graph backfill failed", "error", err)
		}
		if err := handlers.BackfillAccountAIResponsesEnabled(db, lo); err != nil {
			lo.Fatal("Account AI responses backfill failed", "error", err)
		}
	}

	// Caches, state, scheduler and broker: embedded unless [redis] host is set
	stores, err := store.New(&cfg.Redis, db)
	if err != nil {
		lo.Fatal("Failed to connect to Redis; remove [redis] host to use the built-in store",
			"error", err, "host", cfg.Redis.Host, "port", cfg.Redis.Port)
	}
	defer stores.Close()
	if stores.Embedded() {
		if missing := store.MissingTables(db); len(missing) > 0 {
			lo.Fatal("Built-in store tables are missing; run `whatomate server -migrate` once", "tables", strings.Join(missing, ", "))
		}
		lo.Info("Using built-in store (no Redis server)")
	} else {
		lo.Info("Connected to Redis")
		warnLeftoverRows(db, lo)
	}

	// Initialize job queue
	var jobQueue queue.Queue
	var dbQueue *queue.DBQueue
	if stores.Embedded() {
		dbQueue = queue.NewDBQueue(db, lo)
		jobQueue = dbQueue
	} else {
		jobQueue = queue.NewRedisQueue(stores.Redis, lo)
	}
	lo.Info("Job queue initialized")

	// Initialize Fastglue
	g := fastglue.NewGlue()

	// Initialize WhatsApp client
	waClient := whatsapp.NewWithBaseURL(lo, cfg.WhatsApp.BaseURL)

	// Initialize WebSocket hub
	wsHub := websocket.NewHub(lo)
	go wsHub.Run()
	lo.Info("WebSocket hub started")

	// Initialize app with dependencies
	// Shared HTTP client with connection pooling for external API calls
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:         handlers.SSRFSafeDialer(),
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	app := &handlers.App{
		Config:      cfg,
		ConfigPath:  cfgPath,
		DB:          db,
		Cache:       stores.Cache,
		State:       stores.State,
		Scheduler:   stores.Scheduler,
		Broker:      stores.Broker,
		Log:         lo,
		WhatsApp:    waClient,
		WSHub:       wsHub,
		Queue:       jobQueue,
		HTTPClient:  httpClient,
		Maintenance: gate,
		Version:     Version,
	}

	// Initialize recording storage (S3 when configured, otherwise local_path)
	var s3Client *storage.S3Client
	var recordings storage.Uploader
	if cfg.Calling.RecordingEnabled {
		if cfg.Storage.S3Bucket != "" {
			var err error
			s3Client, err = storage.NewS3Client(&cfg.Storage)
			if err != nil {
				lo.Warn("Failed to initialize S3 client for recordings, falling back to local storage", "error", err)
			} else {
				recordings = s3Client
				lo.Info("S3 client initialized for call recordings", "bucket", cfg.Storage.S3Bucket)
			}
		}
		if recordings == nil {
			recordings = storage.NewLocalFS(cfg.Storage.LocalPath)
			lo.Info("Call recordings stored locally", "path", cfg.Storage.LocalPath)
		}
	}

	// Initialize shared assignment engine (used by both chat and call transfers)
	assigner := assignment.New(db, stores.Cache, lo)
	app.Assigner = assigner

	// Initialize CallManager (per-org calling_enabled DB setting controls access)
	app.CallManager = calling.NewManager(&cfg.Calling, recordings, db, stores.Cache, waClient, wsHub, assigner, lo)
	app.CallManager.SetSecrets(cfg.App.EncryptionKey, &cfg.AI)
	app.S3Client = s3Client
	lo.Info("Call manager initialized")

	audioDir := cfg.Calling.AudioDir
	if written, err := defaultaudio.Install(audioDir); err != nil {
		lo.Warn("Could not install the bundled call audio", "dir", audioDir, "error", err)
	} else if len(written) > 0 {
		lo.Info("Installed bundled call audio", "dir", audioDir, "files", written)
	}

	// Initialize TTS router (Piper local + OpenAI/ElevenLabs cloud; keys come per-call from org settings)
	ttsRouter := &tts.Router{
		DefaultProvider: "piper",
		OpenAI: &tts.OpenAISynth{
			AudioDir: audioDir,
		},
		ElevenLabs: &tts.ElevenLabsSynth{
			AudioDir: audioDir,
		},
	}
	if cfg.TTS.PiperBinary != "" && cfg.TTS.PiperModel != "" {
		piper := &tts.PiperTTS{
			BinaryPath:    cfg.TTS.PiperBinary,
			ModelPath:     cfg.TTS.PiperModel,
			OpusencBinary: cfg.TTS.OpusencBinary,
			AudioDir:      audioDir,
		}
		ttsRouter.Piper = piper
		lo.Info("TTS Piper initialized", "piper", cfg.TTS.PiperBinary, "model", cfg.TTS.PiperModel)
	}
	app.TTS = ttsRouter
	app.CallManager.SetTTS(ttsRouter)
	lo.Info("TTS router attached (piper/openai/elevenlabs)")

	// Start campaign stats subscriber for real-time WebSocket updates from worker
	if err := app.StartCampaignStatsSubscriber(); err != nil {
		lo.Error("Failed to start campaign stats subscriber", "error", err)
	}

	// Parse allowed origins for CORS
	allowedOrigins := middleware.ParseAllowedOrigins(cfg.Server.AllowedOrigins)

	// Setup middleware (CORS is handled by corsWrapper at fasthttp level)
	g.Before(middleware.SecurityHeaders())
	g.Before(middleware.RequestLogger(lo))
	g.Before(middleware.Recovery(lo))
	g.Before(middleware.CSRFProtection())

	// Setup routes
	setupRoutes(g, app, lo, cfg.Server.BasePath, stores.Counter, cfg)

	// Create server with CORS wrapper
	server := &fasthttp.Server{
		Handler:            corsWrapper(g.Handler(), allowedOrigins),
		ReadTimeout:        time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout:       time.Duration(cfg.Server.WriteTimeout) * time.Second,
		MaxRequestBodySize: 15 * 1024 * 1024,
		Name:               "Whatomate",
		HeaderReceived: func(h *fasthttp.RequestHeader) fasthttp.RequestConfig {
			switch {
			case handlers.IsBackupDownload(string(h.Method()), string(h.RequestURI())):
				return fasthttp.RequestConfig{WriteTimeout: 6 * time.Hour}
			case handlers.LongUploadRead(h):
				return fasthttp.RequestConfig{ReadTimeout: 5 * time.Minute}
			}
			return fasthttp.RequestConfig{}
		},
	}

	// Start server in goroutine
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	go func() {
		var err error
		if handoff != nil {
			lo.Info("Server listening", "address", handoff.Addr().String())
			err = server.Serve(handoff)
		} else {
			ln, lerr := listenApp(addr, cfgPath)
			if lerr != nil {
				lo.Fatal("Server failed", "error", lerr)
			}
			lo.Info("Server listening", "address", addr)
			err = server.Serve(ln)
		}
		if err != nil {
			lo.Fatal("Server failed", "error", err)
		}
	}()

	// Start SLA processor (runs every minute)
	slaProcessor := handlers.NewSLAProcessor(app, time.Minute)
	slaCtx, slaCancel := context.WithCancel(context.Background())
	go slaProcessor.Start(slaCtx)
	lo.Info("SLA processor started")

	// Resume chatbot Delay nodes without blocking webhooks
	delayProcessor := handlers.NewChatDelayProcessor(app, time.Second)
	delayCtx, delayCancel := context.WithCancel(context.Background())
	go delayProcessor.Start(delayCtx)
	lo.Info("Chat delay processor started")

	deliveryPruner := handlers.NewWebhookDeliveryPruner(app, cfg.Retention.WebhookDeliveryDays, 24*time.Hour)
	pruneCtx, pruneCancel := context.WithCancel(context.Background())
	go deliveryPruner.Start(pruneCtx)

	// Start embedded workers
	var workers []*worker.Worker
	var workerCancel context.CancelFunc
	if *numWorkers > 0 {
		var workerCtx context.Context
		workerCtx, workerCancel = context.WithCancel(context.Background())

		for i := 0; i < *numWorkers; i++ {
			consumer, err := newConsumer(stores, dbQueue, lo)
			if err != nil {
				lo.Fatal("Failed to create worker", "error", err, "worker_num", i+1)
			}
			w := worker.New(cfg, db, consumer, stores.Broker, lo)
			w.Gate = gate
			workers = append(workers, w)

			workerNum := i + 1
			go func() {
				lo.Info("Worker started", "worker_num", workerNum)
				if err := w.Run(workerCtx); err != nil && err != context.Canceled {
					lo.Error("Worker error", "error", err, "worker_num", workerNum)
				}
			}()
		}
		lo.Info("Embedded workers started", "count", *numWorkers)
	} else {
		lo.Info("Embedded workers disabled, run workers separately")
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	lo.Info("Shutting down...")

	// Stop campaign stats subscriber
	lo.Info("Stopping campaign stats subscriber...")
	app.StopCampaignStatsSubscriber()
	lo.Info("Campaign stats subscriber stopped")

	// Stop SLA processor
	lo.Info("Stopping SLA processor...")
	slaCancel()
	slaProcessor.Stop()
	lo.Info("SLA processor stopped")

	lo.Info("Stopping chat delay processor...")
	delayCancel()
	delayProcessor.Stop()
	lo.Info("Chat delay processor stopped")

	pruneCancel()
	deliveryPruner.Stop()

	// Stop workers first
	if workerCancel != nil {
		lo.Info("Stopping workers...", "count", len(workers))
		workerCancel()
		for _, w := range workers {
			_ = w.Close()
		}
		lo.Info("Workers stopped")
	}

	// The server keeps answering /api/maintenance while a restore finishes.
	switch kind, running := app.RunningBackupJob(); {
	case running && kind == "restore":
		lo.Info("Waiting for the running restore to finish (press Ctrl-C again to stop now)")
		stop := make(chan struct{})
		go func() {
			<-quit
			close(stop)
		}()
		if !app.WaitForRestore(stop) {
			lo.Warn("Stopped without waiting for the restore; uploaded files may be half restored. Restore the Before restore backup if needed")
		}
	case running:
		lo.Info("Abandoning the running backup; its temporary file is removed at the next start")
	}

	// Then stop server
	lo.Info("Stopping server...")
	if err := server.Shutdown(); err != nil {
		lo.Error("Server shutdown error", "error", err)
	}
	lo.Info("Server stopped")
}

// ============================================================================
// WORKER COMMAND
// ============================================================================

func runWorker(args []string) {
	workerFlags := flag.NewFlagSet("worker", flag.ExitOnError)
	configPath := workerFlags.String("config", "", "Path to config file (default: search standard locations)")
	workerCount := workerFlags.Int("workers", 1, "Number of workers to run")
	_ = workerFlags.Parse(args)

	// Initialize logger
	lo := logf.New(logf.Opts{
		EnableColor:     true,
		Level:           logf.DebugLevel,
		EnableCaller:    true,
		TimestampFormat: "2006-01-02 15:04:05",
		DefaultFields:   []any{"app", "whatomate-worker"},
	})

	lo.Info("Starting Whatomate worker...", "version", Version)

	cfg, _ := loadConfig(*configPath, lo)

	// Set log level based on environment
	if cfg.App.Environment == "production" {
		lo = logf.New(logf.Opts{
			Level:           logf.InfoLevel,
			TimestampFormat: "2006-01-02 15:04:05",
			DefaultFields:   []any{"app", "whatomate-worker"},
		})
	}

	// Connect to the database
	db, err := database.Open(&cfg.Database, cfg.App.Debug)
	if err != nil {
		lo.Fatal("Failed to connect to database", "error", err)
	}
	lo.Info("Connected to database", "driver", cfg.Database.Driver)

	// Caches, state, scheduler and broker: embedded unless [redis] host is set
	stores, err := store.New(&cfg.Redis, db)
	if err != nil {
		lo.Fatal("Failed to connect to Redis; remove [redis] host to use the built-in store",
			"error", err, "host", cfg.Redis.Host, "port", cfg.Redis.Port)
	}
	defer stores.Close()
	var dbQueue *queue.DBQueue
	if stores.Embedded() {
		if missing := store.MissingTables(db); len(missing) > 0 {
			lo.Fatal("Built-in store tables are missing; run `whatomate server -migrate` once", "tables", strings.Join(missing, ", "))
		}
		dbQueue = queue.NewDBQueue(db, lo)
		lo.Info("Using built-in store (no Redis server)")
		lo.Info("No Redis configured: live campaign progress is not pushed to the UI from separate worker processes")
	} else {
		lo.Info("Connected to Redis")
		warnLeftoverRows(db, lo)
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Create and run workers
	workers := make([]*worker.Worker, *workerCount)
	errCh := make(chan error, *workerCount)

	for i := 0; i < *workerCount; i++ {
		consumer, err := newConsumer(stores, dbQueue, lo)
		if err != nil {
			lo.Fatal("Failed to create worker", "error", err, "worker_num", i+1)
		}
		w := worker.New(cfg, db, consumer, stores.Broker, lo)
		workers[i] = w

		go func(workerNum int) {
			lo.Info("Worker started", "worker_num", workerNum)
			errCh <- w.Run(ctx)
		}(i + 1)
	}

	lo.Info("Workers started", "count", *workerCount)

	// Wait for shutdown signal or error
	select {
	case sig := <-quit:
		lo.Info("Received shutdown signal", "signal", sig)
		cancel()
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			lo.Error("Worker error", "error", err)
			cancel()
		}
	}

	// Cleanup
	lo.Info("Shutting down workers...")
	for _, w := range workers {
		if w != nil {
			if err := w.Close(); err != nil {
				lo.Error("Error closing worker", "error", err)
			}
		}
	}
	lo.Info("Workers stopped")
}

// runSetup serves the first-run setup wizard. It returns the setup socket
// for the app to keep serving on, or nil when the app binds its own port.
func runSetup(flagPath string, lo logf.Logger) net.Listener {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, err := setup.Run(ctx, setup.Options{
		Version: Version,
		Target:  config.Target(flagPath),
		Port:    config.DefaultPort,
		Log:     lo,

		SharedDataDir: setup.DefaultSharedDataDir(runtime.GOOS),
	})
	if errors.Is(err, context.Canceled) {
		lo.Info("Setup cancelled; nothing was written")
		os.Exit(0)
	}
	if err != nil {
		lo.Fatal("Setup failed", "error", err)
	}
	return res.Listener
}

// listenApp binds addr as fasthttp's ListenAndServe does; the error names the
// config file to edit.
func listenApp(addr, cfgPath string) (net.Listener, error) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s: %w; change [server] port in %s or stop the program using that port", addr, err, cfgPath)
	}
	return ln, nil
}

// loadConfig finds and loads the config file, anchoring relative paths in a
// config from the user or system folder to its base folder. It returns the
// config and the file it came from.
func loadConfig(flagPath string, lo logf.Logger) (*config.Config, string) {
	src, err := config.Locate(flagPath)
	if errors.Is(err, config.ErrNotFound) {
		lo.Fatal("No config file found. Run `whatomate server` once to open the setup wizard", "error", err)
	}
	if err != nil {
		lo.Fatal("Failed to find config", "error", err)
	}
	cfg, err := config.Load(src.Path)
	if err != nil {
		lo.Fatal("Failed to load config", "path", src.Path, "error", err)
	}
	if src.BaseDir != "" {
		if err := os.MkdirAll(src.BaseDir, 0o750); err != nil {
			lo.Fatal("Failed to create data folder", "path", src.BaseDir, "error", err)
		}
		cfg.ResolvePaths(src.BaseDir)
	}
	lo.Info("Loaded config", "path", src.Path, "data_dir", src.BaseDir)
	return cfg, src.Path
}

// runConfig handles "whatomate config path [-config file]".
func runConfig(args []string) {
	if len(args) == 0 || args[0] != "path" {
		fmt.Println("Usage: whatomate config path [-config file]")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("config path", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file")
	_ = fs.Parse(args[1:])
	src, err := config.Locate(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(src.Path)
	if src.BaseDir != "" {
		fmt.Println("relative paths resolve against:", src.BaseDir)
	}
}

// warnLeftoverRows logs built-in store rows that Redis mode leaves unprocessed.
func warnLeftoverRows(db *gorm.DB, lo logf.Logger) {
	for table, n := range store.LeftoverRows(db) {
		lo.Warn("Rows left from the built-in store are not processed in Redis mode", "table", table, "rows", n)
	}
}

// newConsumer returns a campaign job consumer for the active store mode.
func newConsumer(stores *store.Stores, dbQueue *queue.DBQueue, lo logf.Logger) (queue.Consumer, error) {
	if stores.Embedded() {
		return dbQueue.NewConsumer(), nil
	}
	return queue.NewRedisConsumer(stores.Redis, lo)
}

// ============================================================================
// ROUTES
// ============================================================================

func setupRoutes(g *fastglue.Fastglue, app *handlers.App, lo logf.Logger, basePath string, counter store.Counter, cfg *config.Config) {
	// Health check
	g.GET("/health", app.HealthCheck)
	g.GET("/ready", app.ReadyCheck)
	g.GET("/api/setup/status", app.SetupStatus)

	g.GET("/api/embedded-signup/config", app.GetEmbeddedSignupConfig)

	// Auth routes (public, optionally rate-limited)
	if cfg.RateLimit.Enabled {
		window := time.Duration(cfg.RateLimit.WindowSeconds) * time.Second
		lo.Info("Rate limiting enabled on auth endpoints",
			"login_max", cfg.RateLimit.LoginMaxAttempts,
			"register_max", cfg.RateLimit.RegisterMaxAttempts,
			"refresh_max", cfg.RateLimit.RefreshMaxAttempts,
			"sso_max", cfg.RateLimit.SSOMaxAttempts,
			"window_seconds", cfg.RateLimit.WindowSeconds)

		g.POST("/api/auth/login", withRateLimit(app.Login, middleware.RateLimitOpts{
			Counter: counter, Log: lo, Max: cfg.RateLimit.LoginMaxAttempts, Window: window, KeyPrefix: "login", TrustProxy: cfg.RateLimit.TrustProxy,
		}))
		g.POST("/api/auth/register", withRateLimit(app.Register, middleware.RateLimitOpts{
			Counter: counter, Log: lo, Max: cfg.RateLimit.RegisterMaxAttempts, Window: window, KeyPrefix: "register", TrustProxy: cfg.RateLimit.TrustProxy,
		}))
		g.POST("/api/auth/refresh", withRateLimit(app.RefreshToken, middleware.RateLimitOpts{
			Counter: counter, Log: lo, Max: cfg.RateLimit.RefreshMaxAttempts, Window: window, KeyPrefix: "refresh", TrustProxy: cfg.RateLimit.TrustProxy,
		}))
		// Begin only issues a challenge, and the login page requests one on every
		// load for passkey autofill, so it gets more headroom than finish.
		g.POST("/api/auth/passkey/begin", withRateLimit(app.BeginPasskeyLogin, middleware.RateLimitOpts{
			Counter: counter, Log: lo, Max: cfg.RateLimit.LoginMaxAttempts * 6, Window: window, KeyPrefix: "passkey_begin", TrustProxy: cfg.RateLimit.TrustProxy,
		}))
		g.POST("/api/auth/passkey/finish", withRateLimit(app.FinishPasskeyLogin, middleware.RateLimitOpts{
			Counter: counter, Log: lo, Max: cfg.RateLimit.LoginMaxAttempts, Window: window, KeyPrefix: "passkey_finish", TrustProxy: cfg.RateLimit.TrustProxy,
		}))
	} else {
		g.POST("/api/auth/login", app.Login)
		g.POST("/api/auth/register", app.Register)
		g.POST("/api/auth/refresh", app.RefreshToken)
		g.POST("/api/auth/passkey/begin", app.BeginPasskeyLogin)
		g.POST("/api/auth/passkey/finish", app.FinishPasskeyLogin)
	}
	g.POST("/api/auth/logout", app.Logout)
	g.POST("/api/auth/switch-org", app.SwitchOrg)
	g.GET("/api/auth/ws-token", app.GetWSToken)

	// SSO routes (public, optionally rate-limited)
	g.GET("/api/auth/sso/providers", app.GetPublicSSOProviders)
	if cfg.RateLimit.Enabled {
		window := time.Duration(cfg.RateLimit.WindowSeconds) * time.Second
		g.GET("/api/auth/sso/{provider}/init", withRateLimit(app.InitSSO, middleware.RateLimitOpts{
			Counter: counter, Log: lo, Max: cfg.RateLimit.SSOMaxAttempts, Window: window, KeyPrefix: "sso_init", TrustProxy: cfg.RateLimit.TrustProxy,
		}))
		g.GET("/api/auth/sso/{provider}/callback", withRateLimit(app.CallbackSSO, middleware.RateLimitOpts{
			Counter: counter, Log: lo, Max: cfg.RateLimit.SSOMaxAttempts, Window: window, KeyPrefix: "sso_callback", TrustProxy: cfg.RateLimit.TrustProxy,
		}))
	} else {
		g.GET("/api/auth/sso/{provider}/init", app.InitSSO)
		g.GET("/api/auth/sso/{provider}/callback", app.CallbackSSO)
	}

	// Webhook routes (public - for Meta)
	g.GET("/api/webhook", app.WebhookVerify)
	g.POST("/api/webhook", app.WebhookHandler)

	// Public chatbot media for Meta link downloads (UUID filename)
	g.GET("/public/chatbot-media/{id}", app.ServeChatbotMedia)

	// WhatsApp Flow data endpoint (public - encrypted and signed by Meta)
	g.POST("/api/flow-endpoint/{id}", app.FlowEndpoint)

	// WebSocket route (auth via message-based flow after upgrade)
	g.GET("/ws", app.WebSocketHandler)

	g.Before(app.MaintenanceGuard)

	// For protected routes, we'll use a path-based middleware approach
	// Apply auth middleware globally but check path in the middleware
	g.Before(func(r *fastglue.Request) *fastglue.Request {
		// Skip auth for OPTIONS preflight requests (handled by CORS middleware)
		if string(r.RequestCtx.Method()) == "OPTIONS" {
			return r
		}
		path := string(r.RequestCtx.Path())
		// Skip auth for public routes
		if path == "/health" || path == "/ready" || path == "/api/setup/status" || path == "/api/maintenance" ||
			path == "/api/auth/login" || path == "/api/auth/register" || path == "/api/auth/refresh" ||
			path == "/api/auth/logout" || path == "/api/webhook" || path == "/ws" ||
			path == "/api/auth/passkey/begin" || path == "/api/auth/passkey/finish" {
			return r
		}
		// Public chatbot media for Meta (UUID filenames; also under base_path)
		if strings.Contains(path, "/public/chatbot-media/") {
			return r
		}
		// WhatsApp Flow data endpoint (request encryption + Meta signature)
		if strings.HasPrefix(path, "/api/flow-endpoint/") {
			return r
		}
		// Skip auth for SSO routes (they handle their own auth via state tokens)
		if len(path) >= 13 && path[:13] == "/api/auth/sso" {
			return r
		}
		// Google account connect callback (one-time state token)
		if path == "/api/google-credentials/oauth/callback" {
			return r
		}
		// Skip auth for custom action redirects (uses one-time token)
		if len(path) >= 28 && path[:28] == "/api/custom-actions/redirect" {
			return r
		}
		// Apply auth for all other /api routes (supports both JWT and API key)
		if len(path) > 4 && path[:4] == "/api" {
			return middleware.AuthWithDB(app.Config.JWT.Secret, app.DB)(r)
		}
		return r
	})

	// Global rate limit on all /api/ routes, keyed by user ID (or IP if unauthenticated).
	// Runs after auth so the user identity is available.
	if cfg.RateLimit.Enabled {
		apiMax := cfg.RateLimit.APIMaxRequests
		if apiMax == 0 {
			apiMax = 200
		}
		apiWindow := cfg.RateLimit.APIWindowSeconds
		if apiWindow == 0 {
			apiWindow = 60
		}
		apiRL := middleware.UserAwareRateLimit(middleware.RateLimitOpts{
			Counter:    counter,
			Log:        lo,
			Max:        apiMax,
			Window:     time.Duration(apiWindow) * time.Second,
			KeyPrefix:  "api_global",
			TrustProxy: cfg.RateLimit.TrustProxy,
		})
		g.Before(func(r *fastglue.Request) *fastglue.Request {
			path := string(r.RequestCtx.Path())
			if len(path) > 4 && path[:4] == "/api" {
				return apiRL(r)
			}
			return r
		})
	}

	// Role-based access control middleware
	g.Before(func(r *fastglue.Request) *fastglue.Request {
		method := string(r.RequestCtx.Method())

		// Skip OPTIONS preflight requests
		if method == "OPTIONS" {
			return r
		}

		// Route-level permission checks are now handled at the handler level
		// using the granular permission system (HasPermission checks)
		return r
	})

	// Current User (all authenticated users)
	g.GET("/api/me", app.GetCurrentUser)
	g.PUT("/api/me/settings", app.UpdateCurrentUserSettings)
	g.PUT("/api/me/password", app.ChangePassword)
	g.GET("/api/me/passkeys", app.ListPasskeys)
	g.POST("/api/me/passkeys/register/begin", app.BeginPasskeyRegistration)
	g.POST("/api/me/passkeys/register/finish", app.FinishPasskeyRegistration)
	g.DELETE("/api/me/passkeys/{id}", app.DeletePasskey)
	g.PUT("/api/me/availability", app.UpdateAvailability)
	g.GET("/api/me/organizations", app.ListMyOrganizations)

	// User Management (admin only - enforced by middleware)
	g.GET("/api/users", app.ListUsers)
	g.POST("/api/users", app.CreateUser)
	g.GET("/api/users/{id}", app.GetUser)
	g.PUT("/api/users/{id}", app.UpdateUser)
	g.DELETE("/api/users/{id}", app.DeleteUser)

	// Roles & Permissions (admin only - enforced by middleware)
	g.GET("/api/roles", app.ListRoles)
	g.POST("/api/roles", app.CreateRole)
	g.GET("/api/roles/{id}", app.GetRole)
	g.PUT("/api/roles/{id}", app.UpdateRole)
	g.DELETE("/api/roles/{id}", app.DeleteRole)
	g.GET("/api/permissions", app.ListPermissions)

	// API Keys (admin only - enforced by middleware)
	g.GET("/api/api-keys", app.ListAPIKeys)
	g.GET("/api/api-keys/{id}", app.GetAPIKey)
	g.POST("/api/api-keys", app.CreateAPIKey)
	g.PUT("/api/api-keys/{id}", app.UpdateAPIKey)
	g.DELETE("/api/api-keys/{id}", app.DeleteAPIKey)

	// Accounts
	g.GET("/api/accounts", app.ListAccounts)
	g.POST("/api/accounts", app.CreateAccount)
	g.POST("/api/accounts/exchange-token", app.ExchangeToken) // Embedded signup
	g.GET("/api/accounts/{id}", app.GetAccount)
	g.PUT("/api/accounts/{id}", app.UpdateAccount)
	g.DELETE("/api/accounts/{id}", app.DeleteAccount)
	g.POST("/api/accounts/{id}/register", app.RegisterPhoneNumber) // Embedded signup manual/2fa registration
	g.POST("/api/accounts/{id}/test", app.TestAccountConnection)
	g.POST("/api/accounts/{id}/subscribe", app.SubscribeApp)
	g.GET("/api/accounts/{id}/flow-encryption", app.GetFlowEncryption)
	g.POST("/api/accounts/{id}/flow-encryption", app.SetupFlowEncryption)
	g.GET("/api/accounts/{id}/business_profile", app.GetBusinessProfile)
	g.PUT("/api/accounts/{id}/business_profile", app.UpdateBusinessProfile)
	g.GET("/api/accounts/{id}/display_name", app.GetDisplayName)
	g.POST("/api/accounts/{id}/display_name", app.RequestDisplayNameChange)
	g.POST("/api/accounts/{id}/display_name/apply", app.ApplyDisplayName)
	g.POST("/api/accounts/{id}/business_profile/photo", app.UpdateProfilePicture)

	// Contacts
	g.GET("/api/contacts", app.ListContacts)
	g.POST("/api/contacts", app.CreateContact)
	g.GET("/api/contacts/{id}", app.GetContact)
	g.PUT("/api/contacts/{id}", app.UpdateContact)
	g.DELETE("/api/contacts/{id}", app.DeleteContact)
	g.PUT("/api/contacts/{id}/assign", app.AssignContact)
	g.PUT("/api/contacts/{id}/tags", app.UpdateContactTags)
	g.GET("/api/contacts/{id}/session-data", app.GetContactSessionData)

	// Generic Import/Export
	g.POST("/api/export", app.ExportData)
	g.POST("/api/import", app.ImportData)
	g.GET("/api/export/{table}/config", app.GetExportConfig)
	g.GET("/api/import/{table}/config", app.GetImportConfig)

	// Tags
	g.GET("/api/tags", app.ListTags)
	g.POST("/api/tags", app.CreateTag)
	g.PUT("/api/tags/{name}", app.UpdateTag)
	g.DELETE("/api/tags/{name}", app.DeleteTag)

	g.GET("/api/contact-lists", app.ListContactLists)
	g.POST("/api/contact-lists", app.CreateContactList)
	g.PUT("/api/contact-lists/{id}", app.UpdateContactList)
	g.DELETE("/api/contact-lists/{id}", app.DeleteContactList)

	g.GET("/api/google-credentials", app.ListGoogleCredentials)
	g.GET("/api/google-credentials/oauth", app.GetGoogleOAuthStatus)
	g.POST("/api/google-credentials/oauth/start", app.StartGoogleOAuth)
	g.GET("/api/google-credentials/oauth/callback", app.GoogleOAuthCallback)
	g.POST("/api/google-credentials", app.CreateGoogleCredential)
	g.DELETE("/api/google-credentials/{id}", app.DeleteGoogleCredential)

	// Messages
	g.GET("/api/contacts/{id}/messages", app.GetMessages)
	g.POST("/api/contacts/{id}/messages", app.SendMessage)
	g.POST("/api/contacts/{id}/mark-read", app.MarkContactRead)
	g.POST("/api/contacts/{id}/typing", app.SendContactTyping)
	g.POST("/api/contacts/{id}/close-agent-session", app.CloseAgentSession)
	g.POST("/api/contacts/{id}/ai-assist", app.ChatAIAssist)
	g.POST("/api/messages/{id}/transcribe", app.TranscribeMessage)
	g.POST("/api/contacts/{id}/messages/{message_id}/reaction", app.SendReaction)
	g.POST("/api/messages", app.SendMessage) // Legacy route
	g.POST("/api/messages/template", app.SendTemplateMessage)
	g.POST("/api/messages/media", app.SendMediaMessage)
	g.PUT("/api/messages/{id}/read", app.MarkMessageRead)

	// Conversation Notes
	g.GET("/api/contacts/{id}/notes", app.ListConversationNotes)
	g.POST("/api/contacts/{id}/notes", app.CreateConversationNote)
	g.PUT("/api/contacts/{id}/notes/{note_id}", app.UpdateConversationNote)
	g.DELETE("/api/contacts/{id}/notes/{note_id}", app.DeleteConversationNote)

	// Media (serves media files for messages, auth-protected)
	g.GET("/api/media/{message_id}", app.ServeMedia)
	g.GET("/api/flow-media/{org}/{name}", app.ServeFlowMedia)

	// Templates
	g.GET("/api/templates", app.ListTemplates)
	g.POST("/api/templates", app.CreateTemplate)
	g.GET("/api/templates/{id}", app.GetTemplate)
	g.PUT("/api/templates/{id}", app.UpdateTemplate)
	g.DELETE("/api/templates/{id}", app.DeleteTemplate)
	g.POST("/api/templates/sync", app.SyncTemplates)
	g.POST("/api/templates/generate", app.GenerateTemplate)
	g.POST("/api/templates/{id}/publish", app.SubmitTemplate)
	g.POST("/api/templates/upload-media", app.UploadTemplateMedia)

	// WhatsApp Flows
	g.GET("/api/flows", app.ListFlows)
	g.POST("/api/flows", app.CreateFlow)
	g.GET("/api/flows/{id}", app.GetFlow)
	g.PUT("/api/flows/{id}", app.UpdateFlow)
	g.DELETE("/api/flows/{id}", app.DeleteFlow)
	g.POST("/api/flows/{id}/save-to-meta", app.SaveFlowToMeta)
	g.POST("/api/flows/{id}/publish", app.PublishFlow)
	g.POST("/api/flows/{id}/deprecate", app.DeprecateFlow)
	g.POST("/api/flows/{id}/duplicate", app.DuplicateFlow)
	g.POST("/api/flows/sync", app.SyncFlows)
	g.POST("/api/flows/generate", app.GenerateWhatsAppFlow)

	// Bulk Campaigns
	g.GET("/api/campaigns", app.ListCampaigns)
	g.POST("/api/campaigns", app.CreateCampaign)
	g.GET("/api/campaigns/{id}", app.GetCampaign)
	g.PUT("/api/campaigns/{id}", app.UpdateCampaign)
	g.DELETE("/api/campaigns/{id}", app.DeleteCampaign)
	g.POST("/api/campaigns/{id}/start", app.StartCampaign)
	g.POST("/api/campaigns/{id}/pause", app.PauseCampaign)
	g.POST("/api/campaigns/{id}/cancel", app.CancelCampaign)
	g.POST("/api/campaigns/{id}/retry-failed", app.RetryFailed)
	g.GET("/api/campaigns/{id}/progress", app.GetCampaign)
	g.POST("/api/campaigns/{id}/recipients/import", app.ImportRecipients)
	g.GET("/api/campaigns/{id}/recipients", app.GetCampaignRecipients)
	g.DELETE("/api/campaigns/{id}/recipients/{recipientId}", app.DeleteCampaignRecipient)
	g.POST("/api/campaigns/{id}/media", app.UploadCampaignMedia)
	g.GET("/api/campaigns/{id}/media", app.ServeCampaignMedia)

	// Chatbot Settings
	g.GET("/api/chatbot/settings", app.GetChatbotSettings)
	g.PUT("/api/chatbot/settings", app.UpdateChatbotSettings)
	g.GET("/api/ai/openrouter/free-models", app.ListOpenRouterFreeModels)

	// Keyword Rules
	g.GET("/api/chatbot/keywords", app.ListKeywordRules)
	g.POST("/api/chatbot/keywords", app.CreateKeywordRule)
	g.GET("/api/chatbot/keywords/{id}", app.GetKeywordRule)
	g.PUT("/api/chatbot/keywords/{id}", app.UpdateKeywordRule)
	g.DELETE("/api/chatbot/keywords/{id}", app.DeleteKeywordRule)

	// Chatbot Flows
	g.GET("/api/chatbot/flows", app.ListChatbotFlows)
	g.POST("/api/chatbot/flows", app.CreateChatbotFlow)
	g.POST("/api/chatbot/flows/generate", app.GenerateChatbotFlow)
	g.POST("/api/chatbot/media", app.UploadChatbotMedia)
	g.GET("/api/chatbot/flows/{id}", app.GetChatbotFlow)
	g.PUT("/api/chatbot/flows/{id}", app.UpdateChatbotFlow)
	g.POST("/api/chatbot/flows/{id}/publish", app.PublishChatbotFlow)
	g.GET("/api/chatbot/flows/{id}/analytics", app.GetChatbotFlowAnalytics)
	g.DELETE("/api/chatbot/flows/{id}", app.DeleteChatbotFlow)

	// AI Contexts
	g.GET("/api/chatbot/ai-contexts", app.ListAIContexts)
	g.POST("/api/chatbot/ai-contexts", app.CreateAIContext)
	g.GET("/api/chatbot/ai-contexts/{id}", app.GetAIContext)
	g.PUT("/api/chatbot/ai-contexts/{id}", app.UpdateAIContext)
	g.DELETE("/api/chatbot/ai-contexts/{id}", app.DeleteAIContext)

	// Agent Transfers
	g.GET("/api/chatbot/transfers", app.ListAgentTransfers)
	g.POST("/api/chatbot/transfers", app.CreateAgentTransfer)
	g.POST("/api/chatbot/transfers/pick", app.PickNextTransfer)
	g.PUT("/api/chatbot/transfers/{id}/resume", app.ResumeFromTransfer)
	g.PUT("/api/chatbot/transfers/{id}/assign", app.AssignAgentTransfer)

	// Teams (admin/manager - access control in handler)
	g.GET("/api/teams", app.ListTeams)
	g.POST("/api/teams", app.CreateTeam)
	g.GET("/api/teams/{id}", app.GetTeam)
	g.PUT("/api/teams/{id}", app.UpdateTeam)
	g.DELETE("/api/teams/{id}", app.DeleteTeam)
	g.GET("/api/teams/{id}/members", app.ListTeamMembers)
	g.POST("/api/teams/{id}/members", app.AddTeamMember)
	g.DELETE("/api/teams/{id}/members/{member_user_id}", app.RemoveTeamMember)

	// Audit Logs
	g.GET("/api/audit-logs", app.ListAuditLogs)
	g.GET("/api/audit-logs/{id}", app.GetAuditLog)

	// Canned Responses
	g.GET("/api/canned-responses", app.ListCannedResponses)
	g.POST("/api/canned-responses", app.CreateCannedResponse)
	g.POST("/api/canned-responses/generate", app.GenerateCannedResponse)
	g.GET("/api/canned-responses/{id}", app.GetCannedResponse)
	g.PUT("/api/canned-responses/{id}", app.UpdateCannedResponse)
	g.DELETE("/api/canned-responses/{id}", app.DeleteCannedResponse)
	g.POST("/api/canned-responses/{id}/use", app.IncrementCannedResponseUsage)

	// Sessions (admin/debug)
	g.GET("/api/chatbot/sessions", app.ListChatbotSessions)
	g.GET("/api/chatbot/sessions/count", app.CountChatbotSessions)
	g.GET("/api/chatbot/sessions/{id}", app.GetChatbotSession)

	// Analytics
	g.GET("/api/analytics/dashboard", app.GetDashboardStats)
	g.GET("/api/analytics/messages", app.GetMessageAnalytics)
	g.GET("/api/analytics/chatbot", app.GetChatbotAnalytics)
	g.GET("/api/analytics/agents", app.GetAgentAnalytics)
	g.GET("/api/analytics/agents/{id}", app.GetAgentDetails)
	g.GET("/api/analytics/agents/comparison", app.GetAgentComparison)

	// Meta WhatsApp Analytics
	g.GET("/api/analytics/meta", app.GetMetaAnalytics)
	g.GET("/api/analytics/meta/accounts", app.ListMetaAccountsForAnalytics)
	g.POST("/api/analytics/meta/refresh", app.RefreshMetaAnalyticsCache)

	// Widgets (customizable analytics)
	g.GET("/api/widgets", app.ListWidgets)
	g.POST("/api/widgets", app.CreateWidget)
	g.GET("/api/widgets/data-sources", app.GetWidgetDataSources)
	g.POST("/api/widgets/generate", app.GenerateWidget)
	g.GET("/api/widgets/data", app.GetAllWidgetsData)
	g.GET("/api/widgets/{id}", app.GetWidget)
	g.PUT("/api/widgets/{id}", app.UpdateWidget)
	g.DELETE("/api/widgets/{id}", app.DeleteWidget)
	g.GET("/api/widgets/{id}/data", app.GetWidgetData)
	g.POST("/api/widgets/layout", app.SaveWidgetLayout)

	// Organization Settings
	g.GET("/api/org/settings", app.GetOrganizationSettings)
	g.PUT("/api/org/settings", app.UpdateOrganizationSettings)
	g.POST("/api/org/audio", app.UploadOrgAudio)

	// Shared Meta App credentials (super admin only — enforced in handlers)
	g.GET("/api/admin/meta-app", app.GetSharedMetaApp)
	g.PUT("/api/admin/meta-app", app.UpdateSharedMetaApp)
	g.GET("/api/admin/server-settings", app.GetServerSettings)
	g.PUT("/api/admin/server-settings", app.UpdateServerSettings)
	g.GET("/api/maintenance", app.GetMaintenance)
	g.GET("/api/admin/backups", app.ListBackups)
	g.POST("/api/admin/backups", app.CreateBackup)
	g.GET("/api/admin/backup-job", app.GetBackupJob)
	g.GET("/api/admin/backups/{name}/download", app.DownloadBackup)
	g.GET("/api/admin/backups/{name}/inspect", app.InspectBackup)
	g.POST("/api/admin/backups/{name}/restore", app.RestoreBackup)
	g.DELETE("/api/admin/backups/{name}", app.DeleteBackup)
	g.POST("/api/admin/backup-uploads", app.StartBackupUpload)
	g.PUT("/api/admin/backup-uploads/{id}", app.PutBackupUploadChunk)
	g.POST("/api/admin/backup-uploads/{id}/finish", app.FinishBackupUpload)
	g.DELETE("/api/admin/backup-uploads/{id}", app.CancelBackupUpload)

	// Organizations
	g.GET("/api/organizations", app.ListOrganizations)
	g.POST("/api/organizations", app.CreateOrganization)
	g.PUT("/api/organizations/{id}", app.UpdateOrganization)
	g.DELETE("/api/organizations/{id}", app.DeleteOrganization)
	g.GET("/api/organizations/current", app.GetCurrentOrganization)
	g.GET("/api/organizations/members", app.ListOrganizationMembers)
	g.POST("/api/organizations/members", app.AddOrganizationMember)
	g.PUT("/api/organizations/members/{member_id}", app.UpdateOrganizationMemberRole)
	g.DELETE("/api/organizations/members/{member_id}", app.RemoveOrganizationMember)

	// SSO Settings (admin only - enforced by middleware)
	g.GET("/api/settings/sso", app.GetSSOSettings)
	g.PUT("/api/settings/sso/{provider}", app.UpdateSSOProvider)
	g.DELETE("/api/settings/sso/{provider}", app.DeleteSSOProvider)

	// Webhooks
	g.GET("/api/webhooks", app.ListWebhooks)
	g.POST("/api/webhooks", app.CreateWebhook)
	g.GET("/api/webhooks/{id}", app.GetWebhook)
	g.PUT("/api/webhooks/{id}", app.UpdateWebhook)
	g.DELETE("/api/webhooks/{id}", app.DeleteWebhook)
	g.POST("/api/webhooks/{id}/test", app.TestWebhook)

	// Custom Actions
	g.GET("/api/custom-actions", app.ListCustomActions)
	g.POST("/api/custom-actions", app.CreateCustomAction)
	g.POST("/api/custom-actions/generate", app.GenerateCustomAction)
	g.GET("/api/custom-actions/{id}", app.GetCustomAction)
	g.PUT("/api/custom-actions/{id}", app.UpdateCustomAction)
	g.DELETE("/api/custom-actions/{id}", app.DeleteCustomAction)
	g.POST("/api/custom-actions/{id}/execute", app.ExecuteCustomAction)
	g.GET("/api/custom-actions/redirect/{token}", app.CustomActionRedirect)

	// IVR Flows
	g.GET("/api/ivr-flows", app.ListIVRFlows)
	g.GET("/api/ivr-flows/{id}", app.GetIVRFlow)
	g.POST("/api/ivr-flows", app.CreateIVRFlow)
	g.POST("/api/ivr-flows/generate", app.GenerateIVRFlow)
	g.PUT("/api/ivr-flows/{id}", app.UpdateIVRFlow)
	g.DELETE("/api/ivr-flows/{id}", app.DeleteIVRFlow)
	g.POST("/api/ivr-flows/audio", app.UploadIVRAudio)
	g.GET("/api/ivr-flows/audio/{filename}", app.ServeIVRAudio)
	g.POST("/api/ivr-flows/voice-preview", app.PreviewIVRGeminiVoice)

	// Call Logs
	g.GET("/api/call-logs", app.ListCallLogs)
	g.GET("/api/call-logs/{id}", app.GetCallLog)
	g.GET("/api/call-logs/{id}/recording/file", app.ServeCallRecordingFile)
	g.GET("/api/call-logs/{id}/recording", app.GetCallRecording)

	// Call Transfers
	g.GET("/api/call-transfers", app.ListCallTransfers)
	g.GET("/api/call-transfers/{id}", app.GetCallTransfer)
	g.POST("/api/call-transfers/{id}/connect", app.ConnectCallTransfer)
	g.POST("/api/call-transfers/{id}/hangup", app.HangupCallTransfer)
	g.POST("/api/call-transfers/initiate", app.InitiateAgentTransfer)

	// Call Hold
	g.POST("/api/call-logs/{id}/hold", app.HoldCall)
	g.POST("/api/call-logs/{id}/resume", app.ResumeCall)

	// Outgoing Calls
	g.POST("/api/calls/outgoing", app.InitiateOutgoingCall)
	g.POST("/api/calls/outgoing/{id}/hangup", app.HangupOutgoingCall)
	g.POST("/api/calls/permission-request", app.SendCallPermissionRequest)
	g.GET("/api/calls/permission/{contactId}", app.GetCallPermission)
	g.GET("/api/calls/ice-servers", app.GetICEServers)

	// Catalogs
	g.GET("/api/catalogs", app.ListCatalogs)
	g.POST("/api/catalogs", app.CreateCatalog)
	g.GET("/api/catalogs/{id}", app.GetCatalog)
	g.DELETE("/api/catalogs/{id}", app.DeleteCatalog)
	g.POST("/api/catalogs/sync", app.SyncCatalogs)

	// Catalog Products
	g.GET("/api/catalogs/{id}/products", app.ListCatalogProducts)
	g.POST("/api/catalogs/{id}/products", app.CreateCatalogProduct)
	g.GET("/api/products/{id}", app.GetCatalogProduct)
	g.PUT("/api/products/{id}", app.UpdateCatalogProduct)
	g.DELETE("/api/products/{id}", app.DeleteCatalogProduct)

	// Serve embedded frontend (SPA)
	if frontend.IsEmbedded() {
		lo.Info("Serving embedded frontend", "base_path", basePath)
		frontendHandler := frontend.Handler(basePath)
		// Catch-all for frontend routes
		g.GET("/{path:*}", func(r *fastglue.Request) error {
			frontendHandler(r.RequestCtx)
			return nil
		})
		g.GET("/", func(r *fastglue.Request) error {
			frontendHandler(r.RequestCtx)
			return nil
		})
	} else {
		lo.Info("Frontend not embedded, API-only mode")
	}
}

// withRateLimit wraps a handler with the rate limit middleware.
func withRateLimit(handler fastglue.FastRequestHandler, opts middleware.RateLimitOpts) fastglue.FastRequestHandler {
	rl := middleware.RateLimit(opts)
	return func(r *fastglue.Request) error {
		if rl(r) == nil {
			return nil // Rate limited — response already sent.
		}
		return handler(r)
	}
}

// corsWrapper wraps a handler with CORS support at the fasthttp level.
// This ensures CORS headers are set even for auto-handled OPTIONS requests.
func corsWrapper(next fasthttp.RequestHandler, allowedOrigins map[string]bool) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		origin := string(ctx.Request.Header.Peek("Origin"))

		if origin != "" && middleware.IsOriginAllowed(origin, allowedOrigins) {
			ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
			ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
		} else if len(allowedOrigins) == 0 && origin != "" {
			// Development: no whitelist configured
			ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
		}

		ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Organization-ID, X-CSRF-Token")
		ctx.Response.Header.Set("Access-Control-Max-Age", "86400")

		// Handle preflight OPTIONS requests
		if string(ctx.Method()) == "OPTIONS" {
			ctx.SetStatusCode(fasthttp.StatusNoContent)
			return
		}

		next(ctx)
	}
}
