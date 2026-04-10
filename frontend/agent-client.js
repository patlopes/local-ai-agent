// =============================================================================
// Local AI Agent — JavaScript SDK
// =============================================================================
// Drop-in client for integrating with the Local AI Agent from any web app.
//
// Usage:
//   import { AgentClient } from './agent-client.js';
//   const agent = new AgentClient('http://localhost:3333');
//
//   // Health check
//   const health = await agent.health();
//
//   // Chat with streaming
//   await agent.chat(
//     [{ role: 'user', content: 'Hello!' }],
//     'gemma3:1b',
//     (token) => process.stdout.write(token)
//   );
// =============================================================================

export class AgentClient {
  /**
   * @param {string} baseUrl - Agent URL (default: http://localhost:3333)
   * @param {string} [token] - Optional auth token
   */
  constructor(baseUrl = 'http://localhost:3333', token = '') {
    this.baseUrl = baseUrl.replace(/\/$/, '');
    this.token = token;
  }

  /** @returns {Record<string, string>} */
  _headers() {
    const h = { 'Content-Type': 'application/json' };
    if (this.token) h['X-Auth-Token'] = this.token;
    return h;
  }

  // ---------- Health ----------

  /**
   * Check if the agent is running and Ollama is ready.
   * @returns {Promise<{status: string, ollama_ready: boolean, default_model: string, version: string}>}
   */
  async health() {
    const res = await fetch(`${this.baseUrl}/health`, { headers: this._headers() });
    if (!res.ok) throw new Error(`Health check failed: ${res.status}`);
    return res.json();
  }

  /**
   * Wait until the agent is reachable.
   * @param {number} timeoutMs - Max wait time (default: 30s)
   * @param {number} intervalMs - Poll interval (default: 1s)
   * @returns {Promise<boolean>}
   */
  async waitForAgent(timeoutMs = 30000, intervalMs = 1000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      try {
        const h = await this.health();
        if (h.status === 'ok') return true;
      } catch {}
      await new Promise(r => setTimeout(r, intervalMs));
    }
    return false;
  }

  // ---------- Models ----------

  /**
   * List available models.
   * @returns {Promise<{models: Array<{name: string, size: number, modified_at: string}>}>}
   */
  async listModels() {
    const res = await fetch(`${this.baseUrl}/models`, { headers: this._headers() });
    if (!res.ok) throw new Error(`List models failed: ${res.status}`);
    return res.json();
  }

  /**
   * Download a model. Returns when complete.
   * @param {string} model - Model name (e.g., 'gemma3:1b')
   * @param {function} [onProgress] - Optional progress callback
   * @returns {Promise<void>}
   */
  async downloadModel(model, onProgress) {
    const res = await fetch(`${this.baseUrl}/models/download`, {
      method: 'POST',
      headers: this._headers(),
      body: JSON.stringify({ model }),
    });

    if (!res.ok) throw new Error(`Download failed: ${res.status}`);

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop();
      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const data = JSON.parse(line);
          if (onProgress) onProgress(data);
        } catch {}
      }
    }
  }

  // ---------- Chat ----------

  /**
   * Send a chat request with streaming.
   * @param {Array<{role: string, content: string}>} messages
   * @param {string} [model] - Model to use (defaults to agent's default)
   * @param {function} onToken - Called with each text token as it arrives
   * @param {AbortSignal} [signal] - Optional abort signal
   * @returns {Promise<string>} Complete response text
   */
  async chat(messages, model, onToken, signal) {
    const res = await fetch(`${this.baseUrl}/chat`, {
      method: 'POST',
      headers: this._headers(),
      body: JSON.stringify({ model: model || undefined, messages, stream: true }),
      signal,
    });

    if (!res.ok) {
      const body = await res.text();
      throw new Error(`Chat failed (${res.status}): ${body}`);
    }

    return this._readStream(res, (chunk) => {
      if (chunk.message?.content && onToken) {
        onToken(chunk.message.content);
      }
    });
  }

  // ---------- Generate ----------

  /**
   * Generate text from a single prompt (non-chat).
   * @param {string} prompt
   * @param {string} [model]
   * @param {function} onToken
   * @param {AbortSignal} [signal]
   * @returns {Promise<string>}
   */
  async generate(prompt, model, onToken, signal) {
    const res = await fetch(`${this.baseUrl}/generate`, {
      method: 'POST',
      headers: this._headers(),
      body: JSON.stringify({ model: model || undefined, prompt, stream: true }),
      signal,
    });

    if (!res.ok) {
      const body = await res.text();
      throw new Error(`Generate failed (${res.status}): ${body}`);
    }

    return this._readStream(res, (chunk) => {
      if (chunk.response && onToken) {
        onToken(chunk.response);
      }
    });
  }

  // ---------- Internal ----------

  /**
   * Read an NDJSON stream and return complete text.
   * @param {Response} res
   * @param {function} onChunk
   * @returns {Promise<string>}
   */
  async _readStream(res, onChunk) {
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let fullText = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop();

      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const chunk = JSON.parse(line);
          onChunk(chunk);
          // Accumulate text from either chat or generate format
          if (chunk.message?.content) fullText += chunk.message.content;
          else if (chunk.response) fullText += chunk.response;
        } catch (e) {
          console.warn('Stream parse error:', e);
        }
      }
    }

    return fullText;
  }
}

// Auto-export for non-module environments
if (typeof window !== 'undefined') {
  window.AgentClient = AgentClient;
}
