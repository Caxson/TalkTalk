# TalkTalk developer entrypoints. Run `make help` for the list.

BIN        := bin/talktalk
PKG        := github.com/Caxson/TalkTalk
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X $(PKG)/internal/version.Version=$(VERSION)
STATICCHECK_VERSION := 2026.2.1
# cgo + libopus are required: brew install opus pkg-config / apt install libopus-dev pkg-config

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/{printf "  %-10s %s\n",$$1,$$2}' $(MAKEFILE_LIST)

.PHONY: run
run: ## Run the server, loading .env if present
	@set -a; [ -f .env ] && . ./.env; set +a; go run -ldflags "$(LDFLAGS)" ./cmd/talktalk

.PHONY: build
build: ## Build the binary into bin/
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/talktalk

.PHONY: test
test: ## Run tests with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run tests and print per-package coverage
	go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run staticcheck
	GOTOOLCHAIN=$$(go env GOVERSION) go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

.PHONY: test-web
test-web: ## Run the browser client's unit tests (Node 20+)
	node --test internal/web/static/test/*.test.mjs

.PHONY: check
check: fmt-check vet lint test test-web ## Everything CI runs

.PHONY: assets
assets: ## Download the lip-sync model, avatar renderer and sample avatar
	./scripts/fetch-assets.sh

.PHONY: tidy
tidy: ## Tidy go.mod
	go mod tidy

.PHONY: clean
clean: ## Remove build outputs
	rm -rf bin coverage.out
