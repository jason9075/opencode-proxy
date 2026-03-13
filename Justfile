set shell := ["bash", "-euo", "pipefail", "-c"]

@dev:
  go run ./cmd/server

@build:
  mkdir -p ./bin
  go build -o ./bin/opencode-proxy ./cmd/server

@fmt:
  gofmt -w ./cmd/server/*.go

@vet:
  go vet ./cmd/server
