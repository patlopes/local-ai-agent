package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/patlopes/local-ai-agent/config"
)

// Manager controls the lifecycle of the embedded Ollama process.
type Manager struct {
	cfg    *config.Config
	cmd    *exec.Cmd
	mu     sync.Mutex
	logger *slog.Logger
	done   chan struct{} // closed when subprocess exits

	// OnDownloadStart is called when Ollama binary download begins.
	OnDownloadStart func()
	// OnDownloadDone is called when Ollama binary download completes.
	OnDownloadDone func()
}

// NewManager creates a new Ollama lifecycle manager.
func NewManager(cfg *config.Config, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:    cfg,
		logger: logger,
	}
}

// Start launches the Ollama subprocess if it is not already running.
// It first checks whether an existing Ollama instance is reachable;
// if so, it reuses it. Otherwise it starts a new subprocess.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if Ollama is already running (user-installed or previous run)
	if m.isHealthy() {
		m.logger.Info("Ollama is already running", "host", m.cfg.OllamaHost)
		return nil
	}

	binaryPath := m.cfg.OllamaBinaryPath()

	// Check if bundled binary exists; if not, try PATH; if not, auto-download
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		pathBinary, pathErr := exec.LookPath("ollama")
		if pathErr == nil {
			binaryPath = pathBinary
			m.logger.Info("Using system Ollama", "path", binaryPath)
		} else {
			// Auto-download Ollama binary
			m.logger.Info("Ollama not found — downloading automatically", "target", binaryPath)
			if m.OnDownloadStart != nil {
				m.OnDownloadStart()
			}
			if dlErr := downloadOllamaBinary(binaryPath, m.logger); dlErr != nil {
				return fmt.Errorf("ollama binary not found and auto-download failed: %w", dlErr)
			}
			if m.OnDownloadDone != nil {
				m.OnDownloadDone()
			}
			m.logger.Info("Ollama binary ready", "path", binaryPath)
		}
	}

	m.cmd = exec.Command(binaryPath, "serve")

	// Set process group so we can kill the entire tree on shutdown.
	// On Linux this also sets Pdeathsig so Ollama dies if the agent crashes.
	setProcAttr(m.cmd)

	// Build env: set Ollama host, models path, and library path for GPU runners.
	libDir := m.cfg.OllamaLibDir()
	env := append(os.Environ(),
		fmt.Sprintf("OLLAMA_HOST=0.0.0.0:%d", m.cfg.OllamaPort),
		fmt.Sprintf("OLLAMA_MODELS=%s/models", m.cfg.DataDir),
	)
	// Prepend our lib dir to LD_LIBRARY_PATH so Ollama finds CUDA runners.
	if existingLD := os.Getenv("LD_LIBRARY_PATH"); existingLD != "" {
		env = append(env, fmt.Sprintf("LD_LIBRARY_PATH=%s:%s", libDir, existingLD))
	} else {
		env = append(env, fmt.Sprintf("LD_LIBRARY_PATH=%s", libDir))
	}
	m.cmd.Env = env

	// Pipe Ollama output to our logger
	m.cmd.Stdout = &logWriter{logger: m.logger, level: slog.LevelInfo, prefix: "[ollama] "}
	m.cmd.Stderr = &logWriter{logger: m.logger, level: slog.LevelWarn, prefix: "[ollama] "}

	m.logger.Info("Starting Ollama subprocess", "binary", binaryPath, "port", m.cfg.OllamaPort)
	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ollama: %w", err)
	}

	// Track when the subprocess exits
	m.done = make(chan struct{})

	// Wait for Ollama to become healthy
	if err := m.waitForHealthy(30 * time.Second); err != nil {
		m.Stop()
		return fmt.Errorf("ollama failed to become healthy: %w", err)
	}

	m.logger.Info("Ollama is ready", "host", m.cfg.OllamaHost)

	// Monitor subprocess in background
	go m.monitor()

	return nil
}

// Stop gracefully shuts down the Ollama subprocess and all its children.
// It sends SIGTERM to the entire process group (killing runners that hold GPU
// memory), waits for clean exit, and escalates to SIGKILL if needed.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return
	}

	pid := m.cmd.Process.Pid
	m.logger.Info("Stopping Ollama process group", "pid", pid)

	// Step 1: SIGTERM the entire process group (ollama serve + all runner children).
	// This is the key difference — we kill the GROUP, not just the main process.
	if err := killProcGroup(pid); err != nil {
		m.logger.Warn("Failed to SIGTERM process group, falling back to single kill", "error", err)
		_ = m.cmd.Process.Signal(syscall.SIGTERM)
	}

	// Step 2: Wait for clean exit
	select {
	case <-m.done:
		m.logger.Info("Ollama exited cleanly")
	case <-time.After(5 * time.Second):
		// Step 3: Force kill the entire process group
		m.logger.Warn("Ollama did not exit in time, force killing process group")
		if err := forceKillProcGroup(pid); err != nil {
			m.logger.Error("Force kill failed", "error", err)
			_ = m.cmd.Process.Kill()
		}
		select {
		case <-m.done:
		case <-time.After(3 * time.Second):
			m.logger.Error("Ollama process group could not be killed")
		}
	}
	m.cmd = nil
}

// IsRunning checks whether Ollama is reachable.
func (m *Manager) IsRunning() bool {
	return m.isHealthy()
}

// isHealthy checks if Ollama responds to requests.
func (m *Manager) isHealthy() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(m.cfg.OllamaHost + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// waitForHealthy polls until Ollama is responsive or timeout expires.
func (m *Manager) waitForHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		if m.isHealthy() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s waiting for ollama", timeout)
		}
		<-ticker.C
	}
}

// monitor watches the subprocess and signals when it exits.
func (m *Manager) monitor() {
	if m.cmd == nil {
		return
	}
	err := m.cmd.Wait()
	if err != nil {
		m.logger.Error("Ollama subprocess exited unexpectedly", "error", err)
	} else {
		m.logger.Info("Ollama subprocess exited")
	}
	// Signal that the process is done — Stop() waits on this.
	close(m.done)
}

// UnloadModels tells Ollama to unload all loaded models from GPU memory.
// Should be called before Stop() to ensure GPU VRAM is freed.
func (m *Manager) UnloadModels() {
	m.unloadAllModels()
}

// unloadAllModels tells Ollama to unload all loaded models from memory.
// This frees GPU VRAM before we kill the process.
func (m *Manager) unloadAllModels() {
	client := &http.Client{Timeout: 5 * time.Second}

	// List running models via /api/ps
	resp, err := client.Get(m.cfg.OllamaHost + "/api/ps")
	if err != nil {
		m.logger.Warn("Failed to list running models", "error", err)
		return
	}
	defer resp.Body.Close()

	var psResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&psResp); err != nil {
		m.logger.Warn("Failed to parse /api/ps response", "error", err)
		return
	}

	// Unload each model by sending keep_alive=0
	for _, model := range psResp.Models {
		m.logger.Info("Unloading model", "model", model.Name)
		reqBody := fmt.Sprintf(`{"model":%q,"keep_alive":0}`, model.Name)
		req, err := http.NewRequest("POST", m.cfg.OllamaHost+"/api/generate",
			bytes.NewBufferString(reqBody))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		unloadResp, err := client.Do(req)
		if err != nil {
			m.logger.Warn("Failed to unload model", "model", model.Name, "error", err)
			continue
		}
		io.Copy(io.Discard, unloadResp.Body)
		unloadResp.Body.Close()
		m.logger.Info("Model unloaded", "model", model.Name)
	}
}

// EnsureModel checks if the specified model is available, and pulls it if not.
func (m *Manager) EnsureModel(ctx context.Context, model string) error {
	m.logger.Info("Ensuring model is available", "model", model)

	// Check if model exists via /api/show
	client := &http.Client{Timeout: 10 * time.Second}
	showURL := fmt.Sprintf("%s/api/show", m.cfg.OllamaHost)

	req, err := http.NewRequestWithContext(ctx, "POST", showURL, jsonBody(map[string]string{"name": model}))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach ollama: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK {
		m.logger.Info("Model already available", "model", model)
		return nil
	}

	// Pull the model
	m.logger.Info("Model not found, pulling...", "model", model)
	return m.pullModel(ctx, model)
}

// pullModel triggers Ollama to download a model.
func (m *Manager) pullModel(ctx context.Context, model string) error {
	pullURL := fmt.Sprintf("%s/api/pull", m.cfg.OllamaHost)

	req, err := http.NewRequestWithContext(ctx, "POST", pullURL, jsonBody(map[string]string{
		"name": model,
	}))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 0} // No timeout for downloads
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("pull request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pull failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Drain the response (it streams progress)
	_, _ = io.Copy(io.Discard, resp.Body)

	m.logger.Info("Model pulled successfully", "model", model)
	return nil
}

// --- helpers ---

type logWriter struct {
	logger *slog.Logger
	level  slog.Level
	prefix string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if len(msg) > 0 && msg[len(msg)-1] == '\n' {
		msg = msg[:len(msg)-1]
	}
	if msg == "" {
		return len(p), nil
	}
	w.logger.Log(context.Background(), w.level, w.prefix+msg)
	return len(p), nil
}
