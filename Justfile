set shell := ["bash", "-euo", "pipefail", "-c"]

@default:
  just --list

@dev:
  air -c .air.toml

@build:
  mkdir -p ./bin
  go build -o ./bin/opencode-proxy ./cmd/server

@fmt:
  gofmt -w ./cmd/server/*.go

@vet:
  go vet ./cmd/server

@view:
  xdg-open "http://localhost:${PORT:-8888}/viewer" 2>/dev/null || echo "Open: http://localhost:${PORT:-8888}/viewer"

@test-e2e:
  curl --no-buffer -X POST "http://localhost:8888/test/mock/gemini" -H "Content-Type: application/json" -d '{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}'
