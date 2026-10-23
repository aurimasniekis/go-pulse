SHELL        := /usr/bin/env bash
GO           ?= go
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null)
PKG          := github.com/aurimasniekis/go-pulse
GO_LDFLAGS   := -s -w -X $(PKG)/internal/app.Version=$(VERSION) -X $(PKG)/internal/app.Commit=$(COMMIT)
IMAGE        ?= pulsed:local
GITLEAKS     ?= $(GO) run github.com/zricethezav/gitleaks/v8@v8.30.1
GOVULNCHECK  ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0
DEV          := docker compose -f deploy/compose.dev.yaml -p pulse-dev

.DEFAULT_GOAL := help
.PHONY: help web web-dev build build-go test race vet lint vuln secrets web-test check image dev-up dev-down dev-logs dev-reset e2e clean

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---- build -------------------------------------------------------------------

web: ## Build the web UI into internal/ui/dist (embedded into the binary)
	cd web && pnpm install --frozen-lockfile && pnpm build

web-dev: ## Vite dev server (proxies /api to PULSE_API, default 127.0.0.1:8080)
	cd web && pnpm dev

build: web build-go ## Build UI + pulsed + pulse into bin/

build-go: ## Build pulsed and pulse without rebuilding the UI
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(GO_LDFLAGS)" -o bin/pulsed ./cmd/pulsed
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(GO_LDFLAGS)" -o bin/pulse ./cmd/pulse

image: ## Build the container image for the local platform ($(IMAGE))
	docker buildx build --load -t $(IMAGE) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) .

# ---- checks ------------------------------------------------------------------

vet: ## go vet
	$(GO) vet ./...

test: vet race ## go vet + race tests (embedded nats-server)

race: ## Go tests with the race detector
	$(GO) test -race -count=1 ./...

lint: ## golangci-lint
	golangci-lint run ./...

vuln: ## govulncheck
	$(GOVULNCHECK) ./...

secrets: ## gitleaks over the git history
	$(GITLEAKS) git --redact --config .gitleaks.toml .

web-test: ## Web typecheck + vitest
	cd web && pnpm typecheck && pnpm test

check: lint test web-test vuln secrets ## Everything CI runs (except e2e)

# ---- dev stack ---------------------------------------------------------------

dev-up: ## Start the dev stack (init, nats, pulsed, SeaweedFS, pebble)
	$(DEV) up -d --build --wait

dev-down: ## Stop the dev stack
	$(DEV) down

dev-logs: ## Follow dev stack logs
	$(DEV) logs -f --tail=200

dev-reset: ## Stop the dev stack and delete its volumes
	$(DEV) down -v

e2e: ## Playwright against the dev stack (make dev-up first)
	cd web && pnpm e2e

clean: ## Remove build output
	rm -rf bin dist internal/ui/dist/assets internal/ui/dist/index.html
