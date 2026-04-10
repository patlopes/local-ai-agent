package api

import (
	"log/slog"
	"net/http"

	"github.com/patlopes/local-ai-agent/config"
)

// CORSMiddleware allows all origins (open access).
func CORSMiddleware(cfg *config.Config, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Auth-Token")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Max-Age", "86400")

			// Handle preflight
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// AuthMiddleware validates the X-Auth-Token header if a token is configured.
// If no token is configured, all requests pass through.
func AuthMiddleware(cfg *config.Config, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth if no token configured
			if cfg.AuthToken == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Health endpoint is always public
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			token := r.Header.Get("X-Auth-Token")
			if token == "" {
				token = r.URL.Query().Get("token")
			}

			if token != cfg.AuthToken {
				logger.Warn("Unauthorized request", "path", r.URL.Path, "remote", r.RemoteAddr)
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger logs incoming HTTP requests.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger.Debug("Request",
				"method", r.Method,
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"origin", r.Header.Get("Origin"),
			)
			next.ServeHTTP(w, r)
		})
	}
}

// Chain applies middleware in order (first applied = outermost).
func Chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}
