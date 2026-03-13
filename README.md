# opencode-proxy

A Go-based proxy server for opencode that forwards OpenAI-compatible or Gemini requests to upstream providers, while logging streaming output to SQLite and per-session log files.

## Features

- Routes requests by path prefix (`/v1/openai`, `/v1/gemini`, `/v1beta`).
- Streams SSE responses end-to-end while recording text deltas.
- Persists structured data in SQLite and raw text in `logs/<session>.log`.
- Nix + Just workflow for reproducible dev environments.

## Requirements

- Nix (for `nix develop`) or Go 1.22+.
- `just` for task shortcuts (optional).

## Setup

1. Copy env file and fill in keys.

```
cp .env.copy .env
```

2. Enter dev shell.

```
nix develop
```

3. Start the proxy.

```
just dev
```

The server listens on `http://localhost:8888` by default.

## Configuration (.env)

- `PORT`: HTTP listen port (default `8888`).
- `LOG_DIR`: directory for session log files.
- `DATABASE_PATH`: SQLite database path.
- `OPENAI_API_KEY`, `GEMINI_API_KEY`, `COPILOT_API_KEY`: upstream credentials.
- `OPENAI_BASE_URL`, `GEMINI_BASE_URL`, `COPILOT_BASE_URL`: upstream base URLs.
- `OPENAI_BASE_URL` should include `/v1` for OpenAI-compatible requests.
- `RATE_LIMIT_INTERVAL`: sleep duration between requests (e.g. `12s`).
- `DEBUG`: set to `true` to write payloads into `./debug`.

## opencode Client Settings

Use OpenAI-compatible providers that point to path-prefixed routes on the proxy:

```json
{
  "provider": {
    "observe-openai": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Observation (OpenAI)",
      "options": {
        "baseURL": "http://localhost:8888/v1/openai",
        "apiKey": "proxy"
      },
      "models": {
        "gpt-4o": { "name": "GPT-4o" }
      }
    },
    "observe-gemini": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Observation (Gemini)",
      "options": {
        "baseURL": "http://localhost:8888/v1/gemini",
        "apiKey": "proxy"
      },
      "models": {
        "gemini-2.5-pro": { "name": "Gemini 2.5 Pro" },
        "gemini-2.5-flash": { "name": "Gemini 2.5 Flash" },
        "gemini-2.5-flash-lite": { "name": "Gemini 2.5 Flash Lite" },
        "gemini-3.0-flash": { "name": "Gemini 3 Flash" }
      }
    }
  }
}
```

The proxy does not translate payloads. Use `/v1/openai` for OpenAI-compatible upstreams and `/v1/gemini` or `/v1beta` for Gemini native requests.

## Config UI

Visit `http://localhost:8888/config` to view route prefixes and masked API keys from `.env`.

## Test Page

Visit `http://localhost:8888/test` to send a single request and inspect request/response content (supports OpenAI and Gemini SSE).

## Logs

- SQLite: `./data/opencode-proxy.db`
- Session logs: `./logs/<session-id>.log`
- Debug payloads: `./debug/<request-id>-client.json`, `./debug/<request-id>-upstream.json`

`session-id` comes from opencode headers or is generated if missing.

## Development

- Format code: `just fmt`
- Vet code: `just vet`
- Build binary: `just build`

## Health Check

```
curl http://localhost:8888/healthz
```
