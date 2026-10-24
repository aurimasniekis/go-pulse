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
.PHONY: help api test-nft test-acme test-s3 drill web web-dev build build-go test race vet lint vuln secrets web-test check image dev-up dev-down dev-logs dev-reset spike e2e clean

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

api: ## Regenerate api/*.openapi.json and the UI's TypeScript types
	$(GO) run ./cmd/pulsed openapi --out-dir api
	cd web && pnpm gen:api

vet: ## go vet
	$(GO) vet ./...

test: vet race ## go vet + race tests (embedded nats-server)

race: ## Go tests with the race detector
	$(GO) test -race -count=1 ./...

lint: ## golangci-lint
	golangci-lint run ./...

vuln: ## govulncheck
	$(GOVULNCHECK) ./...

secrets: ## gitleaks over the git history (ignores user git config: forced colour breaks its parser)
	GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 $(GITLEAKS) git --redact --config .gitleaks.toml .

web-test: ## Web typecheck + vitest
	cd web && pnpm typecheck && pnpm test

check: lint test web-test vuln secrets ## Everything CI runs (except e2e)

# ---- dev stack ---------------------------------------------------------------

deploy/.dev/master.key:
	@mkdir -p deploy/.dev
	@umask 022; openssl rand -hex 32 > $@
	@echo "generated dev master key $@ (dev only, gitignored)"

deploy/.dev/age.key:
	@mkdir -p deploy/.dev
	@umask 077; $(GO) run ./cmd/pulsed backup keygen > $@ 2>/dev/null
	@grep '^# public key: ' $@ | sed 's/^# public key: //' > deploy/.dev/age.pub
	@echo "generated dev backup identity $@ (dev only, gitignored; restore with it from the Backups page)"

dev-up: deploy/.dev/master.key deploy/.dev/age.key ## Start the dev stack (init, nats, pulsed, alloy, seaweedfs)
	$(DEV) up -d --build --wait

dev-down: ## Stop the dev stack
	$(DEV) down

dev-logs: ## Follow dev stack logs
	$(DEV) logs -f --tail=200

dev-reset: ## Stop the dev stack and delete its volumes
	$(DEV) down -v

test-nft: ## banagent against real nftables in a privileged Linux container (needs Docker)
	./scripts/test-nft.sh

test-acme: ## ACME DNS-01 against pebble, NATS reload and served-cert check (needs Docker)
	./scripts/test-acme.sh

drill: ## Restore drill, the PL-BAK-007 release gate (embedded NATS; no Docker)
	$(GO) test -race -count=1 -v -run TestRestoreDrill ./internal/backup/

test-s3: ## Backups against a real S3 service (SeaweedFS in Docker)
	$(GO) test -tags s3 -race -count=1 -v -run TestS3 ./internal/backup/

spike: ## M2 sizing spike: ~10 GB / 5k subjects into NATS under mem_limit 900m (heavy; run only when intended)
	./scripts/spike.sh

e2e: ## Playwright against the dev stack (make dev-up first)
	cd web && pnpm e2e

clean: ## Remove build output
	rm -rf bin dist internal/ui/dist/assets internal/ui/dist/index.html
