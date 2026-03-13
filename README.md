# opencode-proxy

A Go-based proxy server for opencode that forwards OpenAI-compatible or Gemini requests to upstream providers, while logging streaming output to SQLite and per-session log files.

## Features

- Supports OpenAI, Gemini, and GitHub Copilot upstreams (selectable via `.env`).
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

- `PROVIDER`: `openai`, `gemini`, or `copilot`.
- `PORT`: HTTP listen port (default `8888`).
- `LOG_DIR`: directory for session log files.
- `DATABASE_PATH`: SQLite database path.
- `OPENAI_API_KEY`, `GEMINI_API_KEY`, `COPILOT_API_KEY`: upstream credentials.
- `OPENAI_BASE_URL`, `GEMINI_BASE_URL`, `COPILOT_BASE_URL`: upstream base URLs.

## opencode Client Settings

Edit `~/.config/opencode/opencode.json` to point provider `baseURL` to this proxy:

```json
{
  "provider": {
    "openai": {
      "options": { "baseURL": "http://localhost:8888", "apiKey": "proxy" }
    },
    "google": {
      "options": { "baseURL": "http://localhost:8888", "apiKey": "proxy" }
    },
    "github-copilot": {
      "options": { "baseURL": "http://localhost:8888", "apiKey": "proxy" }
    }
  }
}
```

Select the real upstream by setting `PROVIDER` in `.env`.

## Logs

- SQLite: `./data/opencode-proxy.db`
- Session logs: `./logs/<session-id>.log`

`session-id` comes from opencode headers or is generated if missing.

## Development

- Format code: `just fmt`
- Vet code: `just vet`
- Build binary: `just build`

## Health Check

```
curl http://localhost:8888/healthz
```
