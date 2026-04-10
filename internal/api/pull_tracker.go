package api

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// PullProgress tracks the progress of a single model pull.
type PullProgress struct {
	Model     string  `json:"model"`
	Status    string  `json:"status"`              // "downloading", "verifying", "done", "error"
	Detail    string  `json:"detail,omitempty"`     // e.g. "pulling abc123..."
	Total     int64   `json:"total,omitempty"`      // bytes total
	Completed int64   `json:"completed,omitempty"`  // bytes done
	Percent   int     `json:"percent"`              // 0-100
	Speed     string  `json:"speed,omitempty"`      // human readable
	StartedAt int64   `json:"started_at"`           // unix ms
	UpdatedAt int64   `json:"updated_at"`           // unix ms
	Error     string  `json:"error,omitempty"`
}

// PullTracker tracks all active model pulls server-side, surviving page refreshes.
type PullTracker struct {
	mu    sync.RWMutex
	pulls map[string]*PullProgress // model name -> progress
}

// NewPullTracker creates a new tracker.
func NewPullTracker() *PullTracker {
	return &PullTracker{
		pulls: make(map[string]*PullProgress),
	}
}

// Start begins tracking a model pull.
func (pt *PullTracker) Start(model string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	now := time.Now().UnixMilli()
	pt.pulls[model] = &PullProgress{
		Model:     model,
		Status:    "starting",
		StartedAt: now,
		UpdatedAt: now,
	}
}

// Update sets progress for an active pull. Called from NDJSON stream parsing.
func (pt *PullTracker) Update(model, status string, total, completed int64) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	p, ok := pt.pulls[model]
	if !ok {
		return
	}
	now := time.Now().UnixMilli()
	p.Detail = status
	p.UpdatedAt = now

	if total > 0 && completed > 0 {
		p.Total = total
		p.Completed = completed
		p.Percent = int(float64(completed) / float64(total) * 100)
		p.Status = "downloading"

		// Calculate speed
		elapsed := float64(now-p.StartedAt) / 1000.0
		if elapsed > 0 {
			bps := float64(completed) / elapsed
			if bps > 1024*1024*1024 {
				p.Speed = jsonFloat(bps/(1024*1024*1024)) + " GB/s"
			} else if bps > 1024*1024 {
				p.Speed = jsonFloat(bps/(1024*1024)) + " MB/s"
			} else {
				p.Speed = jsonFloat(bps/1024) + " KB/s"
			}
		}
	} else if status != "" {
		// Non-download phase (verifying, etc.)
		if completed == 0 && total == 0 {
			p.Status = "processing"
		}
	}
}

// Finish marks a pull as done.
func (pt *PullTracker) Finish(model string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	p, ok := pt.pulls[model]
	if !ok {
		return
	}
	p.Status = "done"
	p.Percent = 100
	p.Detail = "Complete"
	p.UpdatedAt = time.Now().UnixMilli()

	// Remove after a short delay (so dashboard can see "done")
	go func() {
		time.Sleep(5 * time.Second)
		pt.mu.Lock()
		delete(pt.pulls, model)
		pt.mu.Unlock()
	}()
}

// SetError marks a pull as failed.
func (pt *PullTracker) SetError(model, errMsg string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	p, ok := pt.pulls[model]
	if !ok {
		return
	}
	p.Status = "error"
	p.Error = errMsg
	p.UpdatedAt = time.Now().UnixMilli()

	// Remove after a delay
	go func() {
		time.Sleep(15 * time.Second)
		pt.mu.Lock()
		delete(pt.pulls, model)
		pt.mu.Unlock()
	}()
}

// Active returns all currently tracked pulls.
func (pt *PullTracker) Active() []PullProgress {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	result := make([]PullProgress, 0, len(pt.pulls))
	for _, p := range pt.pulls {
		result = append(result, *p)
	}
	return result
}

// IsActive returns true if a model is currently being pulled.
func (pt *PullTracker) IsActive(model string) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	p, ok := pt.pulls[model]
	return ok && p.Status != "done" && p.Status != "error"
}

// Snapshot returns all active pulls as JSON.
func (pt *PullTracker) Snapshot() []byte {
	data, _ := json.Marshal(map[string]interface{}{
		"pulls": pt.Active(),
	})
	return data
}

func jsonFloat(f float64) string {
	s := json.Number(fmt.Sprintf("%.1f", f))
	return string(s)
}
