package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config holds all agent configuration.
type Config struct {
	// AgentPort is the port the local agent listens on.
	AgentPort int

	// OllamaPort is the port Ollama listens on.
	OllamaPort int

	// OllamaHost is the full Ollama base URL.
	OllamaHost string

	// AllowedOrigins is the list of origins permitted by CORS.
	AllowedOrigins []string

	// DefaultModel is the model to use if none specified.
	DefaultModel string

	// AuthToken is an optional local authentication token.
	// If empty, auth is disabled.
	AuthToken string

	// DataDir is where models and runtime data are stored.
	DataDir string

	// LogLevel controls verbosity: "debug", "info", "warn", "error".
	LogLevel string
}

// DefaultConfig returns a production-ready default configuration.
// Values can be overridden via environment variables.
func DefaultConfig() *Config {
	cfg := &Config{
		AgentPort:      envInt("AGENT_PORT", 3333),
		OllamaPort:     envInt("OLLAMA_PORT", 11434),
		AllowedOrigins: envList("ALLOWED_ORIGINS", []string{"http://localhost:5173", "http://localhost:3000", "http://localhost:8080", "null"}),
		DefaultModel:   envStr("DEFAULT_MODEL", "gemma3:1b"),
		AuthToken:      envStr("AUTH_TOKEN", ""),
		DataDir:        envStr("DATA_DIR", defaultDataDir()),
		LogLevel:       envStr("LOG_LEVEL", "info"),
	}
	cfg.OllamaHost = fmt.Sprintf("http://localhost:%d", cfg.OllamaPort)
	return cfg
}

// GenerateToken creates a cryptographically random token.
func GenerateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback — should never happen
		return "fallback-local-token-change-me"
	}
	return hex.EncodeToString(b)
}

// OllamaBinaryPath returns the path to the Ollama binary inside
// the DataDir so everything can be cleaned up by deleting one folder.
func (c *Config) OllamaBinaryPath() string {
	binary := "ollama"
	if runtime.GOOS == "windows" {
		binary = "ollama.exe"
	}
	return filepath.Join(c.DataDir, "bin", binary)
}

// OllamaLibDir returns the path to Ollama's runner libraries (CUDA, etc.)
// inside the DataDir.
func (c *Config) OllamaLibDir() string {
	return filepath.Join(c.DataDir, "lib", "ollama")
}

// --- helpers ---

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}

func envList(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local-ai-agent")
}
