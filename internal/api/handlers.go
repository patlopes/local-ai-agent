package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/patlopes/local-ai-agent/config"
	"github.com/patlopes/local-ai-agent/internal/ollama"
	"github.com/patlopes/local-ai-agent/internal/proxy"
)

// Handlers holds the HTTP handler implementations.
type Handlers struct {
	cfg        *config.Config
	ollama     *ollama.Manager
	proxy      *proxy.Proxy
	logger     *slog.Logger
	shutdownCh chan struct{}
	pullTracker *PullTracker
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(cfg *config.Config, om *ollama.Manager, px *proxy.Proxy, logger *slog.Logger, shutdownCh chan struct{}, pt *PullTracker) *Handlers {
	return &Handlers{
		cfg:        cfg,
		ollama:     om,
		proxy:      px,
		logger:     logger,
		shutdownCh: shutdownCh,
		pullTracker: pt,
	}
}

// --- Health ---

// HealthResponse is the JSON returned by /health.
type HealthResponse struct {
	Status       string `json:"status"`
	OllamaReady  bool   `json:"ollama_ready"`
	DefaultModel string `json:"default_model"`
	Version      string `json:"version"`
}

// Health returns the agent's health status.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	resp := HealthResponse{
		Status:       "ok",
		OllamaReady:  h.ollama.IsRunning(),
		DefaultModel: h.cfg.DefaultModel,
		Version:      "1.0.0",
	}

	if !resp.OllamaReady {
		resp.Status = "degraded"
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- Chat ---

// ChatRequest is the expected JSON body for /chat.
type ChatRequest struct {
	Model     string        `json:"model,omitempty"`
	Messages  []ChatMessage `json:"messages"`
	Stream    *bool         `json:"stream,omitempty"`
	KeepAlive interface{}   `json:"keep_alive,omitempty"`
	Think     *bool         `json:"think,omitempty"`
}

// ChatMessage represents a single message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Chat proxies a chat completion request to Ollama with streaming.
func (h *Handlers) Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB limit
	if err != nil {
		h.logger.Error("Failed to read chat request body", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		return
	}

	var chatReq ChatRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	if len(chatReq.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messages array is required"})
		return
	}

	// Default model
	if chatReq.Model == "" {
		chatReq.Model = h.cfg.DefaultModel
	}

	// Default to streaming
	if chatReq.Stream == nil {
		t := true
		chatReq.Stream = &t
	}

	// Keep model loaded in memory (don't unload between requests)
	if chatReq.KeepAlive == nil {
		chatReq.KeepAlive = -1
	}

	// Think is only passed through if the client explicitly requests it.
	// Not all models support thinking (e.g. gemma3:1b will error).
	// The frontend should only set think=true for models known to support it.

	// Re-marshal with defaults applied
	reqBody, err := json.Marshal(chatReq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to marshal request"})
		return
	}

	h.logger.Info("Chat request", "model", chatReq.Model, "messages", len(chatReq.Messages), "stream", *chatReq.Stream)

	if err := h.proxy.ForwardStream(r.Context(), w, "/api/chat", bytes.NewReader(reqBody)); err != nil {
		h.logger.Error("Chat proxy error", "error", err)
		// Can't write error if we already started streaming
	}
}

// --- Models ---

// ListModels returns the list of available models.
func (h *Handlers) ListModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	if err := h.proxy.Forward(r.Context(), w, "/api/tags", http.MethodGet, nil, ""); err != nil {
		h.logger.Error("Failed to list models", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to reach ollama"})
	}
}

// --- Model Download ---

// DownloadModelRequest is the body for /models/download.
type DownloadModelRequest struct {
	Model string `json:"model"`
}

// DownloadModel triggers a model download via Ollama, tracking progress server-side.
func (h *Handlers) DownloadModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	var req DownloadModelRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model field is required"})
		return
	}

	h.logger.Info("Model download requested", "model", req.Model)
	h.pullTracker.Start(req.Model)

	// Make request to Ollama
	pullBody, _ := json.Marshal(map[string]interface{}{
		"name":   req.Model,
		"stream": true,
	})

	targetURL := fmt.Sprintf("%s/api/pull", h.cfg.OllamaHost)
	ollamaReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(pullBody))
	if err != nil {
		h.pullTracker.SetError(req.Model, err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ollamaReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(ollamaReq)
	if err != nil {
		h.pullTracker.SetError(req.Model, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to reach ollama"})
		return
	}
	defer resp.Body.Close()

	// Stream response to client while parsing progress for the tracker
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 4096)
	var lineBuf []byte
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// Write to client
			w.Write(chunk)
			if canFlush {
				flusher.Flush()
			}

			// Parse NDJSON lines for tracker
			lineBuf = append(lineBuf, chunk...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := lineBuf[:idx]
				lineBuf = lineBuf[idx+1:]
				if len(line) == 0 {
					continue
				}
				var progress struct {
					Status    string `json:"status"`
					Total     int64  `json:"total"`
					Completed int64  `json:"completed"`
					Error     string `json:"error"`
				}
				if json.Unmarshal(line, &progress) == nil {
					if progress.Error != "" {
						h.pullTracker.SetError(req.Model, progress.Error)
					} else {
						h.pullTracker.Update(req.Model, progress.Status, progress.Total, progress.Completed)
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	// Mark as done
	h.pullTracker.Finish(req.Model)
	h.logger.Info("Model download complete", "model", req.Model)
}

// ActivePulls returns the status of all active model pulls.
func (h *Handlers) ActivePulls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(h.pullTracker.Snapshot())
}

// --- Generate (single prompt, non-chat) ---

// Generate proxies a generate request to Ollama.
func (h *Handlers) Generate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	// Ensure model default is applied
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if _, ok := reqMap["model"]; !ok {
		reqMap["model"] = h.cfg.DefaultModel
	}
	if _, ok := reqMap["stream"]; !ok {
		reqMap["stream"] = true
	}

	reqBody, _ := json.Marshal(reqMap)

	if err := h.proxy.ForwardStream(r.Context(), w, "/api/generate", bytes.NewReader(reqBody)); err != nil {
		h.logger.Error("Generate proxy error", "error", err)
	}
}

// --- Available Models Catalog ---

// CatalogModel describes an available model for download.
type CatalogModel struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Size        string `json:"size"`
	Params      string `json:"params"`
	Category    string `json:"category"`
}

var modelCatalog = []CatalogModel{
	// Small / fast
	{Name: "gemma3:1b", Description: "Google Gemma 3 — fast and lightweight", Size: "815 MB", Params: "1B", Category: "small"},
	{Name: "gemma4:e2b", Description: "Google Gemma 4 — efficient 2B variant", Size: "1.6 GB", Params: "2B", Category: "small"},
	{Name: "qwen3:1.7b", Description: "Alibaba Qwen 3 — fast multilingual", Size: "1.1 GB", Params: "1.7B", Category: "small"},
	{Name: "llama3.2:1b", Description: "Meta Llama 3.2 — compact general-purpose", Size: "1.3 GB", Params: "1B", Category: "small"},
	{Name: "deepseek-r1:1.5b", Description: "DeepSeek R1 — reasoning focused", Size: "1.1 GB", Params: "1.5B", Category: "small"},
	{Name: "phi4-mini", Description: "Microsoft Phi-4 Mini — efficient SLM", Size: "2.5 GB", Params: "3.8B", Category: "small"},

	// Medium
	{Name: "gemma3:4b", Description: "Google Gemma 3 — balanced quality/speed", Size: "3.3 GB", Params: "4B", Category: "medium"},
	{Name: "gemma4:e4b", Description: "Google Gemma 4 — efficient 4B variant", Size: "3.1 GB", Params: "4B", Category: "medium"},
	{Name: "gemma4:12b", Description: "Google Gemma 4 — next-gen quality", Size: "8.9 GB", Params: "12B", Category: "medium"},
	{Name: "llama3.2:3b", Description: "Meta Llama 3.2 — great all-rounder", Size: "2.0 GB", Params: "3B", Category: "medium"},
	{Name: "qwen3:8b", Description: "Alibaba Qwen 3 — strong multilingual", Size: "5.2 GB", Params: "8B", Category: "medium"},
	{Name: "mistral:7b", Description: "Mistral 7B — solid general purpose", Size: "4.1 GB", Params: "7B", Category: "medium"},
	{Name: "deepseek-r1:8b", Description: "DeepSeek R1 — reasoning at 8B scale", Size: "4.9 GB", Params: "8B", Category: "medium"},

	// Large
	{Name: "gemma3:12b", Description: "Google Gemma 3 — high-quality responses", Size: "8.1 GB", Params: "12B", Category: "large"},
	{Name: "gemma4:31b", Description: "Google Gemma 4 — frontier multimodal", Size: "58 GB", Params: "31B", Category: "large"},
	{Name: "llama3.3:70b", Description: "Meta Llama 3.3 — frontier-class", Size: "43 GB", Params: "70B", Category: "large"},
	{Name: "qwen3:32b", Description: "Alibaba Qwen 3 — large reasoning", Size: "20 GB", Params: "32B", Category: "large"},
	{Name: "deepseek-r1:32b", Description: "DeepSeek R1 — deep reasoning", Size: "20 GB", Params: "32B", Category: "large"},
	{Name: "command-r:35b", Description: "Cohere Command R — RAG & enterprise", Size: "20 GB", Params: "35B", Category: "large"},

	// Code
	{Name: "qwen2.5-coder:7b", Description: "Qwen 2.5 Coder — code generation", Size: "4.7 GB", Params: "7B", Category: "code"},
	{Name: "codellama:7b", Description: "Meta Code Llama — code completion", Size: "3.8 GB", Params: "7B", Category: "code"},
	{Name: "starcoder2:7b", Description: "BigCode StarCoder 2 — multi-language code", Size: "4.0 GB", Params: "7B", Category: "code"},

	// Embedding
	{Name: "nomic-embed-text", Description: "Nomic — high-quality text embeddings", Size: "274 MB", Params: "137M", Category: "embedding"},
	{Name: "mxbai-embed-large", Description: "Mixedbread — large embeddings model", Size: "670 MB", Params: "335M", Category: "embedding"},
}

// AvailableModels returns the curated model catalog.
func (h *Handlers) AvailableModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"models": modelCatalog})
}

// --- Delete Model ---

// DeleteModelRequest is the body for DELETE /models/delete.
type DeleteModelRequest struct {
	Model string `json:"model"`
}

// DeleteModel removes a model from Ollama.
func (h *Handlers) DeleteModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	var req DeleteModelRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model field is required"})
		return
	}

	h.logger.Info("Model delete requested", "model", req.Model)

	delBody, _ := json.Marshal(map[string]string{"name": req.Model})

	if err := h.proxy.Forward(r.Context(), w, "/api/delete", http.MethodDelete, bytes.NewReader(delBody), "application/json"); err != nil {
		h.logger.Error("Model delete error", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to delete model"})
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(w, `{"error":"encoding error"}`)
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

// --- System Info ---

// GPUInfo holds detected GPU information.
type GPUInfo struct {
	Available   bool   `json:"available"`
	Name        string `json:"name,omitempty"`
	VRAM        string `json:"vram,omitempty"`
	VRAMUsed    string `json:"vram_used,omitempty"`
	Utilization string `json:"utilization,omitempty"`
	Driver      string `json:"driver,omitempty"`
	CUDAVersion string `json:"cuda_version,omitempty"`
}

// SystemInfoResponse is the JSON for /system.
type SystemInfoResponse struct {
	GPU         GPUInfo `json:"gpu"`
	CPUCores    int     `json:"cpu_cores"`
	GOOS        string  `json:"goos"`
	GOARCH      string  `json:"goarch"`
	DataDir     string  `json:"data_dir"`
	DataSizeMB  int64   `json:"data_size_mb"`
	OllamaBin   string  `json:"ollama_bin"`
	OllamaLibs  bool    `json:"ollama_libs"`
}

// SystemInfo returns system/hardware information including GPU detection.
func (h *Handlers) SystemInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	info := SystemInfoResponse{
		GPU:        detectGPU(),
		CPUCores:   runtime.NumCPU(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		DataDir:    h.cfg.DataDir,
		OllamaBin:  h.cfg.OllamaBinaryPath(),
		OllamaLibs: dirExists(h.cfg.OllamaLibDir()),
	}

	// Calculate data directory size
	info.DataSizeMB = dirSizeMB(h.cfg.DataDir)

	writeJSON(w, http.StatusOK, info)
}

// detectGPU tries nvidia-smi to find NVIDIA GPU info.
func detectGPU() GPUInfo {
	gpu := GPUInfo{}

	// Try nvidia-smi
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=name,memory.total,memory.used,utilization.gpu,driver_version",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return gpu
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		return gpu
	}

	// Parse: "NVIDIA GeForce RTX 4070 SUPER, 12282, 1234, 45, 560.35.03"
	parts := strings.SplitN(line, ", ", 5)
	gpu.Available = true
	if len(parts) >= 1 {
		gpu.Name = parts[0]
	}
	if len(parts) >= 2 {
		mb, _ := strconv.ParseFloat(parts[1], 64)
		if mb >= 1024 {
			gpu.VRAM = fmt.Sprintf("%.1f GB", mb/1024)
		} else {
			gpu.VRAM = fmt.Sprintf("%.0f MB", mb)
		}
	}
	if len(parts) >= 3 {
		mb, _ := strconv.ParseFloat(parts[2], 64)
		if mb >= 1024 {
			gpu.VRAMUsed = fmt.Sprintf("%.1f GB", mb/1024)
		} else {
			gpu.VRAMUsed = fmt.Sprintf("%.0f MB", mb)
		}
	}
	if len(parts) >= 4 {
		gpu.Utilization = strings.TrimSpace(parts[3]) + "%"
	}
	if len(parts) >= 5 {
		gpu.Driver = strings.TrimSpace(parts[4])
	}

	// Try to get CUDA version
	cudaOut, err := exec.Command("nvidia-smi", "--query-gpu=compute_cap", "--format=csv,noheader").Output()
	if err == nil {
		gpu.CUDAVersion = strings.TrimSpace(string(cudaOut))
	}

	return gpu
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func dirSizeMB(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total / (1024 * 1024)
}

// --- Shutdown ---

// Shutdown triggers a graceful agent shutdown.
func (h *Handlers) Shutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	h.logger.Info("Shutdown requested via API")
	writeJSON(w, http.StatusOK, map[string]string{"status": "shutting_down"})

	// Signal shutdown in a goroutine so the response is sent first
	go func() {
		h.shutdownCh <- struct{}{}
	}()
}
