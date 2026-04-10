package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/patlopes/local-ai-agent/config"
)

// Proxy forwards requests from the agent to the Ollama backend
// and streams responses back to the client.
type Proxy struct {
	cfg    *config.Config
	client *http.Client
	logger *slog.Logger
}

// New creates a new proxy instance.
func New(cfg *config.Config, logger *slog.Logger) *Proxy {
	return &Proxy{
		cfg:    cfg,
		client: &http.Client{Timeout: 0}, // No timeout — streaming
		logger: logger,
	}
}

// Forward sends a request to Ollama and streams the response back.
// It preserves the request body and method.
func (p *Proxy) Forward(ctx context.Context, w http.ResponseWriter, ollamaPath string, method string, body io.Reader, contentType string) error {
	targetURL := fmt.Sprintf("%s%s", p.cfg.OllamaHost, ollamaPath)
	p.logger.Debug("Proxying request", "method", method, "url", targetURL)

	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return fmt.Errorf("failed to create proxy request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("proxy request failed: %w", err)
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Stream the response body to the client
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				p.logger.Debug("Client disconnected during streaming", "error", writeErr)
				return nil // Client went away, not an error on our side
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("error reading ollama response: %w", readErr)
		}
	}

	return nil
}

// ForwardStream is a convenience for POST endpoints that need SSE-style streaming.
// It sets appropriate headers and delegates to Forward.
func (p *Proxy) ForwardStream(ctx context.Context, w http.ResponseWriter, ollamaPath string, body io.Reader) error {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering if behind reverse proxy

	return p.Forward(ctx, w, ollamaPath, http.MethodPost, body, "application/json")
}
