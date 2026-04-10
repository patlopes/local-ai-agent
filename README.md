# Local AI Agent

Run AI locally with a single binary. No setup, no cloud, no accounts.

Local AI Agent is a self-contained desktop application that downloads and manages [Ollama](https://ollama.com) behind the scenes, gives you a browser-based dashboard to chat, manage models, and monitor your system — all from one executable.

## Features

- **Zero setup** — download one binary, run it, done. Ollama is downloaded automatically on first launch.
- **Built-in dashboard** — model library, live chat, system monitor, and logs at `http://localhost:3333`.
- **GPU acceleration** — automatically detects NVIDIA GPUs and uses CUDA. Falls back to CPU if no GPU is found.
- **Model library** — browse and download from 24+ curated models (Llama, Gemma, Qwen, Phi, DeepSeek, Mistral, and more).
- **Thinking/reasoning** — models like Qwen3, QwQ, and DeepSeek-R1 show their chain-of-thought reasoning alongside answers.
- **Cross-platform** — runs on Linux, macOS (Intel + Apple Silicon), and Windows.
- **Streaming chat** — real-time token-by-token responses via NDJSON streaming.
- **Clean shutdown** — unloads models and frees GPU VRAM before exiting.

## Quick Start

### Download

Grab the latest binary for your platform from the [Releases](https://github.com/patlopes/local-ai-agent/releases) page, or visit the built-in download page at `/download` when the agent is running.

### Run

```bash
# Linux / macOS
chmod +x local-ai-agent-*
./local-ai-agent-*

# Windows
local-ai-agent-windows-amd64.exe
```

That's it. The agent will:

1. Download Ollama (if not already present)
2. Start it as a managed subprocess
3. Pull the default model (`gemma3:1b`)
4. Open your browser to `http://localhost:3333`

Everything is stored in `~/.local-ai-agent/` (binary, models, GPU libraries).

### Flags

```
--no-browser    Don't open the browser on startup
```

## Dashboard

The embedded dashboard at `http://localhost:3333` includes:

- **Chat** — conversation interface with markdown rendering, code highlighting, and thinking/reasoning display
- **Model Library** — browse, search, filter, download, and delete models
- **System Info** — GPU detection, CPU cores, disk usage, data directory
- **Live Logs** — real-time server log stream
- **Boot Progress** — SSE-based startup status

A separate full-page chat UI is also available at `frontend/index.html`.

## API

All endpoints are served on `http://localhost:3333`:

| Method | Path                | Description                              |
|--------|---------------------|------------------------------------------|
| GET    | `/health`           | Agent and Ollama status                  |
| POST   | `/chat`             | Chat completion (streaming NDJSON)       |
| POST   | `/generate`         | Text generation (streaming NDJSON)       |
| GET    | `/models`           | List downloaded models                   |
| POST   | `/models/download`  | Pull a model (streaming progress)        |
| POST   | `/models/delete`    | Delete a downloaded model                |
| GET    | `/models/available` | Curated model catalog                    |
| GET    | `/models/pulling`   | Active download progress                 |
| GET    | `/system`           | System info (GPU, CPU, disk)             |
| GET    | `/logs`             | Live log stream (SSE)                    |
| GET    | `/boot-status`      | Boot progress stream (SSE)               |
| POST   | `/shutdown`         | Graceful shutdown                        |
| GET    | `/`                 | Dashboard UI                             |
| GET    | `/download`         | OS-aware download page                   |

### Examples

**Chat:**
```bash
curl -N http://localhost:3333/chat \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [{"role": "user", "content": "Hello!"}],
    "model": "gemma3:1b"
  }'
```

**List models:**
```bash
curl http://localhost:3333/models
```

**Pull a model:**
```bash
curl -N http://localhost:3333/models/download \
  -H "Content-Type: application/json" \
  -d '{"model": "llama3.2:3b"}'
```

## Configuration

Environment variables (all optional):

| Variable        | Default             | Description                       |
|-----------------|---------------------|-----------------------------------|
| `AGENT_PORT`    | `3333`              | Port for the agent                |
| `OLLAMA_PORT`   | `11434`             | Port for Ollama subprocess        |
| `DEFAULT_MODEL` | `gemma3:1b`         | Model to pull on first run        |
| `DATA_DIR`      | `~/.local-ai-agent` | Where everything is stored        |
| `AUTH_TOKEN`    | *(disabled)*        | Optional API auth token           |
| `LOG_LEVEL`     | `info`              | `debug`, `info`, `warn`, `error`  |

## Building from Source

Requires Go 1.22+.

```bash
# Current platform
CGO_ENABLED=0 go build -ldflags "-s -w" -trimpath -o local-ai-agent .

# All platforms
./scripts/build.sh all
```

Outputs go to `bin/` (binaries) and `dist/` (packaged archives).

## Releases

Releases are automated via GitHub Actions. Pushing a tag triggers cross-compilation for all 6 targets:

| Platform             | Architecture |
|----------------------|--------------|
| Linux                | amd64, arm64 |
| macOS                | amd64, arm64 |
| Windows              | amd64, arm64 |

```bash
git tag v1.0.1 && git push origin v1.0.1
```

## Project Structure

```
local-ai-agent/
├── main.go                          # Entry point, lifecycle, signals
├── config/config.go                 # Environment-based configuration
├── internal/
│   ├── api/
│   │   ├── server.go                # HTTP server, routes, embedded HTML
│   │   ├── handlers.go              # API handlers + model catalog
│   │   ├── middleware.go            # CORS, auth, logging
│   │   ├── dashboard.html           # Embedded dashboard UI
│   │   ├── download.html            # OS-aware download page
│   │   ├── boot_status.go          # Boot progress SSE
│   │   ├── pull_tracker.go         # Server-side download tracking
│   │   └── log_stream.go           # Live log streaming
│   ├── ollama/
│   │   ├── manager.go              # Ollama subprocess lifecycle
│   │   ├── download.go             # Ollama binary downloader
│   │   ├── proc_linux.go           # Linux process groups
│   │   ├── proc_darwin.go          # macOS process groups
│   │   ├── proc_windows.go         # Windows process handling
│   │   └── helpers.go              # JSON utilities
│   └── proxy/proxy.go              # Streaming request proxy
├── frontend/index.html              # Standalone chat UI
├── scripts/build.sh                 # Cross-platform build script
└── .github/workflows/release.yml    # CI/CD release pipeline
```

## How It Works

```
┌──────────┐     ┌──────────────────┐     ┌─────────┐
│ Browser  │────▶│ Local AI Agent   │────▶│ Ollama  │
│          │◀────│ localhost:3333   │◀────│ :11434  │
└──────────┘     └──────────────────┘     └─────────┘
                  Single Go binary         Managed subprocess
                  Dashboard + API          Auto-downloaded
                  CORS · Auth · Proxy      GPU-accelerated
```

The agent acts as a proxy — Ollama is never exposed directly. The agent manages its full lifecycle: download, start, health checks, model management, and clean shutdown with GPU memory release.

## License

MIT
