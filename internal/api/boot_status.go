package api

import (
	"encoding/json"
	"sync"
	"time"
)

// BootPhase represents a stage in the boot process.
type BootPhase string

const (
	PhaseInit         BootPhase = "init"
	PhaseOllamaCheck  BootPhase = "ollama_check"
	PhaseOllamaDownload BootPhase = "ollama_download"
	PhaseOllamaStart  BootPhase = "ollama_start"
	PhaseModelCheck   BootPhase = "model_check"
	PhaseModelPull    BootPhase = "model_pull"
	PhaseReady        BootPhase = "ready"
	PhaseError        BootPhase = "error"
)

// BootStep is a single step shown in the UI.
type BootStep struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"` // "pending", "running", "done", "error"
	Detail  string `json:"detail,omitempty"`
	StartAt int64  `json:"start_at,omitempty"`
	EndAt   int64  `json:"end_at,omitempty"`
}

// BootStatus tracks the agent's boot progress for the dashboard.
type BootStatus struct {
	mu        sync.RWMutex
	Phase     BootPhase   `json:"phase"`
	Ready     bool        `json:"ready"`
	Steps     []BootStep  `json:"steps"`
	listeners []chan []byte
}

// NewBootStatus creates a tracker with predefined steps.
func NewBootStatus() *BootStatus {
	return &BootStatus{
		Phase: PhaseInit,
		Steps: []BootStep{
			{ID: "ollama_check", Label: "Checking for Ollama", Status: "pending"},
			{ID: "ollama_download", Label: "Downloading Ollama", Status: "pending"},
			{ID: "ollama_start", Label: "Starting Ollama", Status: "pending"},
			{ID: "model_check", Label: "Checking default model", Status: "pending"},
			{ID: "model_pull", Label: "Downloading model", Status: "pending"},
			{ID: "ready", Label: "Agent ready", Status: "pending"},
		},
	}
}

// SetStep updates a step's status and detail, then broadcasts to listeners.
func (b *BootStatus) SetStep(id, status, detail string) {
	b.mu.Lock()
	for i := range b.Steps {
		if b.Steps[i].ID == id {
			b.Steps[i].Status = status
			b.Steps[i].Detail = detail
			now := time.Now().UnixMilli()
			if status == "running" && b.Steps[i].StartAt == 0 {
				b.Steps[i].StartAt = now
			}
			if status == "done" || status == "error" || status == "skipped" {
				b.Steps[i].EndAt = now
			}
			break
		}
	}
	snapshot := b.snapshot()
	b.mu.Unlock()

	b.broadcast(snapshot)
}

// SkipStep marks a step as skipped.
func (b *BootStatus) SkipStep(id, reason string) {
	b.SetStep(id, "skipped", reason)
}

// MarkReady sets the overall status to ready.
func (b *BootStatus) MarkReady() {
	b.mu.Lock()
	b.Phase = PhaseReady
	b.Ready = true
	b.mu.Unlock()
	b.SetStep("ready", "done", "All systems operational")
}

// Subscribe returns a channel that receives JSON snapshots on each update.
func (b *BootStatus) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.listeners = append(b.listeners, ch)
	// Send current state immediately
	snapshot := b.snapshot()
	b.mu.Unlock()
	ch <- snapshot
	return ch
}

// Unsubscribe removes a listener channel.
func (b *BootStatus) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, c := range b.listeners {
		if c == ch {
			b.listeners = append(b.listeners[:i], b.listeners[i+1:]...)
			close(ch)
			return
		}
	}
}

// Snapshot returns the current status as JSON.
func (b *BootStatus) Snapshot() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.snapshot()
}

func (b *BootStatus) snapshot() []byte {
	data, _ := json.Marshal(struct {
		Phase BootPhase  `json:"phase"`
		Ready bool       `json:"ready"`
		Steps []BootStep `json:"steps"`
	}{
		Phase: b.Phase,
		Ready: b.Ready,
		Steps: b.Steps,
	})
	return data
}

func (b *BootStatus) broadcast(data []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.listeners {
		select {
		case ch <- data:
		default:
			// Drop if listener is slow
		}
	}
}
