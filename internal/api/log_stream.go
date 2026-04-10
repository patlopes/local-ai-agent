package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

const maxLogEntries = 500

// LogEntry is a single structured log line.
type LogEntry struct {
	Time    string         `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// LogStream captures slog records into a ring buffer and broadcasts to SSE listeners.
type LogStream struct {
	mu        sync.RWMutex
	entries   []LogEntry
	listeners []chan LogEntry
}

// NewLogStream creates a new log stream.
func NewLogStream() *LogStream {
	return &LogStream{
		entries: make([]LogEntry, 0, maxLogEntries),
	}
}

// Push adds a log entry and broadcasts it to all listeners.
func (ls *LogStream) Push(entry LogEntry) {
	ls.mu.Lock()
	if len(ls.entries) >= maxLogEntries {
		// Shift ring buffer
		ls.entries = ls.entries[1:]
	}
	ls.entries = append(ls.entries, entry)
	listeners := make([]chan LogEntry, len(ls.listeners))
	copy(listeners, ls.listeners)
	ls.mu.Unlock()

	for _, ch := range listeners {
		select {
		case ch <- entry:
		default:
			// Drop if listener is slow
		}
	}
}

// Recent returns the last n entries (or all if n > len).
func (ls *LogStream) Recent(n int) []LogEntry {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	if n <= 0 || n > len(ls.entries) {
		n = len(ls.entries)
	}
	start := len(ls.entries) - n
	out := make([]LogEntry, n)
	copy(out, ls.entries[start:])
	return out
}

// Subscribe returns a channel that receives new log entries.
func (ls *LogStream) Subscribe() chan LogEntry {
	ch := make(chan LogEntry, 64)
	ls.mu.Lock()
	ls.listeners = append(ls.listeners, ch)
	ls.mu.Unlock()
	return ch
}

// Unsubscribe removes a listener.
func (ls *LogStream) Unsubscribe(ch chan LogEntry) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for i, c := range ls.listeners {
		if c == ch {
			ls.listeners = append(ls.listeners[:i], ls.listeners[i+1:]...)
			close(ch)
			return
		}
	}
}

// --- slog.Handler implementation ---

// LogStreamHandler is a slog.Handler that writes to both a LogStream and a fallback handler.
type LogStreamHandler struct {
	stream   *LogStream
	fallback slog.Handler
	attrs    []slog.Attr
	groups   []string
}

// NewLogStreamHandler creates a handler that tees logs to the stream and fallback.
func NewLogStreamHandler(stream *LogStream, fallback slog.Handler) *LogStreamHandler {
	return &LogStreamHandler{
		stream:   stream,
		fallback: fallback,
	}
}

func (h *LogStreamHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.fallback.Enabled(ctx, level)
}

func (h *LogStreamHandler) Handle(ctx context.Context, r slog.Record) error {
	// Build entry
	entry := LogEntry{
		Time:    r.Time.Format(time.RFC3339Nano),
		Level:   r.Level.String(),
		Message: r.Message,
	}

	// Collect pre-set attrs from WithAttrs
	attrs := make(map[string]any)
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}

	// Collect record attrs
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		for _, g := range h.groups {
			key = g + "." + key
		}
		attrs[key] = a.Value.Any()
		return true
	})

	if len(attrs) > 0 {
		entry.Attrs = attrs
	}

	h.stream.Push(entry)
	return h.fallback.Handle(ctx, r)
}

func (h *LogStreamHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogStreamHandler{
		stream:   h.stream,
		fallback: h.fallback.WithAttrs(attrs),
		attrs:    append(h.attrs, attrs...),
		groups:   h.groups,
	}
}

func (h *LogStreamHandler) WithGroup(name string) slog.Handler {
	return &LogStreamHandler{
		stream:   h.stream,
		fallback: h.fallback.WithGroup(name),
		attrs:    h.attrs,
		groups:   append(h.groups, name),
	}
}

// SerializeEntry converts a LogEntry to JSON bytes.
func SerializeEntry(e LogEntry) []byte {
	data, _ := json.Marshal(e)
	return data
}
