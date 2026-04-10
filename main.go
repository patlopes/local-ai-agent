package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/patlopes/local-ai-agent/config"
	"github.com/patlopes/local-ai-agent/internal/api"
	"github.com/patlopes/local-ai-agent/internal/ollama"
)

const banner = `
╔══════════════════════════════════════╗
║        Local AI Agent v1.0.0         ║
║    Your private AI, running local    ║
╚══════════════════════════════════════╝
`

func main() {
	// --- Flags ---
	showHelp := flag.Bool("help", false, "Show help")
	genToken := flag.Bool("gen-token", false, "Generate a random auth token and exit")
	noBrowser := flag.Bool("no-browser", false, "Don't auto-open the dashboard in the browser")
	flag.Parse()

	if *showHelp {
		printUsage()
		os.Exit(0)
	}

	if *genToken {
		fmt.Println(config.GenerateToken())
		os.Exit(0)
	}

	// --- Config ---
	cfg := config.DefaultConfig()

	// --- Logger ---
	logLevel := parseLogLevel(cfg.LogLevel)
	logStream := api.NewLogStream()
	textHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})
	logger := slog.New(api.NewLogStreamHandler(logStream, textHandler))
	slog.SetDefault(logger)

	fmt.Print(banner)
	logger.Info("Configuration loaded",
		"agent_port", cfg.AgentPort,
		"ollama_port", cfg.OllamaPort,
		"default_model", cfg.DefaultModel,
		"data_dir", cfg.DataDir,
		"allowed_origins", cfg.AllowedOrigins,
	)

	// --- Ensure data directory ---
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		logger.Error("Failed to create data directory", "path", cfg.DataDir, "error", err)
		os.Exit(1)
	}

	// --- Context for shutdown ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Create Ollama manager (don't start yet) ---
	ollamaMgr := ollama.NewManager(cfg, logger)

	// --- Start HTTP server FIRST so dashboard is immediately available ---
	shutdownCh := make(chan struct{}, 1)
	server := api.NewServer(cfg, ollamaMgr, logger, shutdownCh, logStream)

	serverErr := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	dashboardURL := fmt.Sprintf("http://localhost:%d", cfg.AgentPort)
	logger.Info("Dashboard available",
		"dashboard", dashboardURL,
	)

	// Auto-open dashboard in default browser immediately
	if !*noBrowser {
		go openBrowser(dashboardURL)
	}

	// --- Boot Ollama and models in background (dashboard shows progress) ---
	boot := server.BootStatus

	// Wire download callbacks
	ollamaMgr.OnDownloadStart = func() {
		boot.SetStep("ollama_download", "running", "Downloading Ollama binary... this may take a few minutes")
	}
	ollamaMgr.OnDownloadDone = func() {
		boot.SetStep("ollama_download", "done", "Ollama binary downloaded")
	}

	go func() {
		// Step 1: Check for Ollama
		boot.SetStep("ollama_check", "running", "Looking for Ollama binary...")
		time.Sleep(300 * time.Millisecond) // Brief pause so user sees the step

		// Step 2: Start Ollama (may trigger download)
		boot.SetStep("ollama_check", "done", "Binary located")

		boot.SetStep("ollama_start", "running", "Launching Ollama subprocess...")
		if err := ollamaMgr.Start(ctx); err != nil {
			errMsg := err.Error()
			// Check if it was a download issue
			if contains(errMsg, "auto-download") || contains(errMsg, "not found") {
				boot.SetStep("ollama_download", "error", errMsg)
				boot.SetStep("ollama_start", "error", "Cannot start without Ollama binary")
			} else {
				boot.SkipStep("ollama_download", "Not needed")
				boot.SetStep("ollama_start", "error", errMsg)
			}
			logger.Error("Failed to start Ollama", "error", err)
			// Don't return — dashboard still works in degraded mode
			boot.SkipStep("model_check", "Ollama not available")
			boot.SkipStep("model_pull", "Ollama not available")
			boot.SetStep("ready", "error", "Running in degraded mode — Ollama unavailable")
			return
		}
		boot.SkipStep("ollama_download", "Already present")
		boot.SetStep("ollama_start", "done", fmt.Sprintf("Running on port %d", cfg.OllamaPort))

		// Step 3: Check/pull default model
		boot.SetStep("model_check", "running", fmt.Sprintf("Looking for %s...", cfg.DefaultModel))
		if err := ollamaMgr.EnsureModel(ctx, cfg.DefaultModel); err != nil {
			boot.SetStep("model_check", "done", "Model not found locally")
			boot.SetStep("model_pull", "error", err.Error())
			logger.Warn("Failed to ensure default model", "model", cfg.DefaultModel, "error", err)
			boot.SetStep("ready", "done", "Ready (default model may need manual download)")
			boot.MarkReady()
			return
		}
		boot.SetStep("model_check", "done", "Model available")
		boot.SkipStep("model_pull", "Already downloaded")

		// All good!
		boot.MarkReady()
		logger.Info("Agent fully ready",
			"dashboard", dashboardURL,
			"health", fmt.Sprintf("%s/health", dashboardURL),
		)
	}()

	// --- Wait for shutdown signal ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("Received shutdown signal", "signal", sig)
	case <-shutdownCh:
		logger.Info("Shutdown requested via API")
	case err := <-serverErr:
		logger.Error("Server error", "error", err)
	}

	// --- Graceful shutdown ---
	logger.Info("Shutting down...")

	// 1. Unload models to free GPU VRAM (while Ollama is still running)
	ollamaMgr.UnloadModels()

	// 2. Kill Ollama process and all children
	ollamaMgr.Stop()
	cancel()

	// 3. Shutdown HTTP server (should be fast now, no active proxies)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("Server shutdown timeout (force closing)", "error", err)
	}

	logger.Info("Goodbye!")
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func printUsage() {
	fmt.Println(`Local AI Agent — run local AI models with a simple HTTP API

Usage:
  local-ai-agent [flags]

Flags:
  --help         Show this help message
  --gen-token    Generate a random authentication token

Environment Variables:
  AGENT_PORT        Port for the agent API (default: 3333)
  OLLAMA_PORT       Port for Ollama backend (default: 11434)
  ALLOWED_ORIGINS   Comma-separated list of allowed CORS origins
  DEFAULT_MODEL     Default LLM model (default: gemma3:1b)
  AUTH_TOKEN        Optional authentication token
  DATA_DIR          Data directory (default: ~/.local-ai-agent)
  LOG_LEVEL         Log level: debug, info, warn, error (default: info)

Endpoints:
  GET  /health           Health check
  POST /chat             Chat completion (streaming)
  POST /generate         Text generation (streaming)
  GET  /models           List available models
  POST /models/download  Download a model
  POST /shutdown         Shutdown the agent`)
}

// openBrowser opens the given URL in the default browser.
// Works on Windows, macOS, and Linux.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default: // linux, freebsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
