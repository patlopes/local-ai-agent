package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/patlopes/local-ai-agent/config"
	"github.com/patlopes/local-ai-agent/internal/ollama"
	"github.com/patlopes/local-ai-agent/internal/proxy"
)

//go:embed dashboard.html
var dashboardHTML []byte

// Server is the agent's HTTP server.
type Server struct {
	cfg        *config.Config
	srv        *http.Server
	logger     *slog.Logger
	BootStatus *BootStatus
	LogStream  *LogStream
}

// NewServer constructs and configures the HTTP server with all routes and middleware.
func NewServer(cfg *config.Config, om *ollama.Manager, logger *slog.Logger, shutdownCh chan struct{}, logStream *LogStream) *Server {
	px := proxy.New(cfg, logger)
	pullTracker := NewPullTracker()
	handlers := NewHandlers(cfg, om, px, logger, shutdownCh, pullTracker)
	bootStatus := NewBootStatus()

	mux := http.NewServeMux()

	// Register routes
	mux.HandleFunc("/health", handlers.Health)
	mux.HandleFunc("/chat", handlers.Chat)
	mux.HandleFunc("/generate", handlers.Generate)
	mux.HandleFunc("/models", handlers.ListModels)
	mux.HandleFunc("/models/download", handlers.DownloadModel)
	mux.HandleFunc("/models/delete", handlers.DeleteModel)
	mux.HandleFunc("/models/available", handlers.AvailableModels)
	mux.HandleFunc("/models/pulling", handlers.ActivePulls)
	mux.HandleFunc("/shutdown", handlers.Shutdown)
	mux.HandleFunc("/system", handlers.SystemInfo)

	// Logs SSE endpoint
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		// EventSource sends Accept: text/event-stream, but also support plain GET for streaming
		if accept == "text/event-stream" || r.URL.Query().Get("stream") != "" || r.Header.Get("Cache-Control") == "no-cache" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}

			// Send recent history first
			for _, entry := range logStream.Recent(100) {
				fmt.Fprintf(w, "data: %s\n\n", SerializeEntry(entry))
			}
			flusher.Flush()

			ch := logStream.Subscribe()
			defer logStream.Unsubscribe(ch)

			ctx := r.Context()
			for {
				select {
				case entry, ok := <-ch:
					if !ok {
						return
					}
					fmt.Fprintf(w, "data: %s\n\n", SerializeEntry(entry))
					flusher.Flush()
				case <-ctx.Done():
					return
				}
			}
		}

		// Plain JSON: return recent entries
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"logs": logStream.Recent(200)})
	})

	// Boot status SSE endpoint
	mux.HandleFunc("/boot-status", func(w http.ResponseWriter, r *http.Request) {
		// SSE stream
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}

			ch := bootStatus.Subscribe()
			defer bootStatus.Unsubscribe(ch)

			ctx := r.Context()
			for {
				select {
				case data, ok := <-ch:
					if !ok {
						return
					}
					fmt.Fprintf(w, "data: %s\n\n", data)
					flusher.Flush()
				case <-ctx.Done():
					return
				}
			}
		}

		// Plain JSON snapshot
		w.Header().Set("Content-Type", "application/json")
		w.Write(bootStatus.Snapshot())
	})

	// Embedded dashboard UI (served at root)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(dashboardHTML)
	})

	// Apply middleware stack
	handler := Chain(mux,
		RequestLogger(logger),
		CORSMiddleware(cfg, logger),
		AuthMiddleware(cfg, logger),
	)

	return &Server{
		cfg:        cfg,
		BootStatus: bootStatus,
		LogStream:  logStream,
		srv: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.AgentPort),
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		logger: logger,
	}
}

// Start begins listening. Blocks until the server shuts down.
func (s *Server) Start() error {
	s.logger.Info("Agent HTTP server starting",
		"addr", s.srv.Addr,
		"allowed_origins", s.cfg.AllowedOrigins,
	)
	if s.cfg.AuthToken != "" {
		s.logger.Info("Authentication enabled (X-Auth-Token required)")
	} else {
		s.logger.Info("Authentication disabled (no AUTH_TOKEN set)")
	}
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Agent HTTP server shutting down")
	return s.srv.Shutdown(ctx)
}
