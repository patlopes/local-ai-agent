# Local AI Agent

A production-ready local AI agent that runs on your machine, embeds Ollama, and exposes a secure HTTP API for browser-based frontends.

## Architecture

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   Browser UI    │────▶│  Local AI Agent   │────▶│     Ollama      │
│  (any website)  │◀────│  localhost:3333   │◀────│ localhost:11434  │
└─────────────────┘     └──────────────────┘     └─────────────────┘
                         CORS · Auth · Proxy      Managed subprocess
```

**Key design decisions:**

- **Go single binary** — zero runtime dependencies, compiles for all platforms
- **Ollama embedded** — bundled as a subprocess, users never interact with it directly
- **Proxy architecture** — Ollama is never exposed to the browser; the agent validates, enriches, and forwards all requests
- **Streaming first** — all LLM responses use NDJSON streaming for real-time token delivery
- **Security by default** — CORS restricted to configured origins, optional auth token

## Quick Start

### 1. Download Ollama binary

```bash
make download-ollama
# or manually:
./scripts/download-ollama.sh
```

### 2. Run the agent

```bash
make run
# or:
go run .
```

The agent will:
1. Start Ollama as a subprocess
2. Wait for it to be healthy
3. Ensure the default model (`gemma3:1b`) is downloaded
4. Start the HTTP API on `http://localhost:3333`

### 3. Open the frontend

Open `frontend/index.html` in your browser, or integrate using the JS client.

## API Endpoints

| Method | Path               | Description                          |
|--------|--------------------|--------------------------------------|
| GET    | `/health`          | Agent & Ollama status                |
| POST   | `/chat`            | Chat completion (streaming NDJSON)   |
| POST   | `/generate`        | Text generation (streaming NDJSON)   |
| GET    | `/models`          | List available models                |
| POST   | `/models/download` | Download/pull a model                |

### Examples

**Health check:**
```bash
curl http://localhost:3333/health
```

**Chat (streaming):**
```bash
curl -N http://localhost:3333/chat \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "user", "content": "Explain quantum computing in 3 sentences"}
    ]
  }'
```

**List models:**
```bash
curl http://localhost:3333/models
```

**Download a model:**
```bash
curl -N http://localhost:3333/models/download \
  -H "Content-Type: application/json" \
  -d '{"model": "llama3.2:1b"}'
```

## Configuration

All configuration is via environment variables:

| Variable          | Default                                        | Description                          |
|-------------------|------------------------------------------------|--------------------------------------|
| `AGENT_PORT`      | `3333`                                         | Port for the agent API               |
| `OLLAMA_PORT`     | `11434`                                        | Port for Ollama backend              |
| `ALLOWED_ORIGINS` | `http://localhost:5173,http://localhost:3000`   | Comma-separated CORS origins         |
| `DEFAULT_MODEL`   | `gemma3:1b`                                    | Default LLM model                    |
| `AUTH_TOKEN`      | *(empty — auth disabled)*                      | Optional auth token                  |
| `DATA_DIR`        | `~/.local-ai-agent`                            | Data directory for models            |
| `LOG_LEVEL`       | `info`                                         | Logging: debug, info, warn, error    |

### Enable authentication

```bash
# Generate a token
./local-ai-agent --gen-token
# → a1b2c3d4e5f6...

# Run with auth enabled
AUTH_TOKEN=a1b2c3d4e5f6... ./local-ai-agent
```

Clients must include `X-Auth-Token: <token>` header on all requests (except `/health`).

## Building

### Current platform

```bash
make build
```

### All platforms

```bash
make build-all
```

Outputs are in `bin/` and packaged distributions in `dist/`.

### Manual cross-compilation

```bash
# Linux
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/local-ai-agent-linux-amd64 .

# macOS (Apple Silicon)
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/local-ai-agent-darwin-arm64 .

# Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/local-ai-agent-windows-amd64.exe .
```

## Distribution

For each target platform:

1. Build the agent binary
2. Download the Ollama binary for that platform:
   ```bash
   ./scripts/download-ollama.sh linux amd64
   ./scripts/download-ollama.sh darwin arm64
   ./scripts/download-ollama.sh windows amd64
   ```
3. Package together:
   ```
   local-ai-agent-linux-amd64/
   ├── local-ai-agent-linux-amd64   # agent binary
   └── ollama/
       └── ollama                    # ollama binary
   ```

The agent looks for `ollama/ollama` (or `ollama/ollama.exe`) relative to its own binary.

## Frontend Integration

### Using the JS SDK

```html
<script src="agent-client.js"></script>
<script>
  const agent = new AgentClient('http://localhost:3333');

  // Check connection
  const health = await agent.health();
  console.log('Agent status:', health.status);

  // Chat with streaming
  await agent.chat(
    [{ role: 'user', content: 'Hello!' }],
    'gemma3:1b',
    (token) => document.body.innerText += token
  );
</script>
```

### Using fetch directly

```javascript
// Detect agent
try {
  const res = await fetch('http://localhost:3333/health');
  const data = await res.json();
  console.log('Agent ready:', data.status === 'ok');
} catch {
  console.log('Agent not running');
}

// Chat with streaming
const response = await fetch('http://localhost:3333/chat', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    messages: [{ role: 'user', content: 'What is 2+2?' }],
  }),
});

const reader = response.body.getReader();
const decoder = new TextDecoder();

while (true) {
  const { done, value } = await reader.read();
  if (done) break;

  const text = decoder.decode(value);
  for (const line of text.split('\n')) {
    if (!line.trim()) continue;
    const chunk = JSON.parse(line);
    if (chunk.message?.content) {
      process.stdout.write(chunk.message.content); // or append to DOM
    }
  }
}
```

## Error Handling

The agent handles these failure scenarios:

| Scenario                  | Behavior                                               |
|---------------------------|--------------------------------------------------------|
| Ollama binary not found   | Checks PATH as fallback; logs clear error              |
| Ollama fails to start     | Agent starts in degraded mode; `/health` reports it    |
| Port 3333 in use          | Exits with clear error; configure via `AGENT_PORT`     |
| Port 11434 in use         | Reuses existing Ollama instance                        |
| Model not available       | Auto-downloads on first use                            |
| No GPU                    | Ollama falls back to CPU automatically                 |
| Client disconnects        | Streaming aborts cleanly                               |

## Project Structure

```
local-ai-agent/
├── main.go                     # Entry point, lifecycle orchestration
├── go.mod
├── Makefile
├── config/
│   └── config.go               # Configuration management
├── internal/
│   ├── ollama/
│   │   ├── manager.go          # Ollama subprocess lifecycle
│   │   └── helpers.go          # JSON utilities
│   ├── api/
│   │   ├── server.go           # HTTP server setup
│   │   ├── handlers.go         # Route handlers
│   │   └── middleware.go       # CORS, auth, logging
│   └── proxy/
│       └── proxy.go            # Request proxying with streaming
├── frontend/
│   ├── index.html              # Example chat UI
│   └── agent-client.js         # JavaScript SDK
├── scripts/
│   ├── build.sh                # Cross-platform build
│   └── download-ollama.sh      # Ollama binary downloader
└── ollama/                     # Ollama binary (gitignored)
```

## License

MIT
