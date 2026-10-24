# Contributing to Pulse

Pulse mints credentials for every tool that talks to it and keeps years of personal event history. Read the
[security page](https://aurimasniekis.github.io/go-pulse/security/) before changing anything: a change must never weaken
a security guarantee, default or check silently. If it relaxes one, say so explicitly in the pull request and the
CHANGELOG `Security` section, and explain why.

> License: Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Contributions are accepted under the same license.

## Development setup

- Go: the version in `go.mod`. CI uses `GOTOOLCHAIN=local` with exactly that version.
- `golangci-lint` v2, `govulncheck`, `gitleaks`.
- Node 24 LTS with pnpm 10, for the dashboard (`web/`), the TypeScript SDK (`sdk/ts`) and the website (`site/`).
- Python ≥ 3.10, for the Python SDK tests.
- Docker, for the dev stack, the e2e suite and the nftables, ACME and S3 tests.

## Everyday commands

| Command | What it does |
|---|---|
| `make build` | Dashboard + `pulsed` + `pulse` into `bin/`. |
| `make test` | `go vet` and every Go test with the race detector (embedded NATS from the generated config). |
| `make lint` | golangci-lint with the repository configuration. |
| `make web-test` | Dashboard typecheck and vitest. |
| `make check` | Everything CI runs except e2e: lint, tests, web tests, govulncheck, gitleaks. |
| `make dev-up` / `make dev-down` | Local stack: init, NATS, pulsed, Alloy, SeaweedFS. Dashboard on http://127.0.0.1:8080. |
| `make e2e` | Playwright against the dev stack. |
| `make sdk-test` / `make test-sdk-interop` | SDK conformance vectors / Go ↔ Python ↔ TypeScript against a real server. |
| `make drill` | The backup restore drill alone (part of `make test`; every release must pass it). |
| `make test-nft` / `make test-acme` / `make test-s3` | banagent against real nftables, ACME against pebble, backups against SeaweedFS. |
| `make api` | Regenerate the OpenAPI specs and the dashboard's TypeScript types. CI fails on drift. |
| `make site-dev` | The documentation website with live reload. |
| `make screenshots` | Regenerate the documentation screenshots from a throwaway stack with fictional data. |

`make spike` writes about 10 GB into a Docker volume. It is a sizing experiment, not a test; run it only on purpose.

## Rules

- Tests accompany every change. Security controls get negative tests that prove the forbidden thing fails. Every bug
  fix adds a regression test.
- Never commit secrets, `.creds`, seeds, master keys or real infrastructure details. Tests generate their keys and
  certificates at run time. Use `events.example.com` and TEST-NET addresses in docs and tests.
- Crypto comes from the Go standard library, `golang.org/x/crypto` and nats-io's nkeys/jwt. No `InsecureSkipVerify`.
- Events live in JetStream and pulsed's state lives in SQLite. No other datastore.
- Never call work done when it is only implemented: say whether it is implemented, tested or externally verified.
- Commits follow Conventional Commits. User-facing changes get a CHANGELOG entry under `[Unreleased]`.

## Reporting security issues

See [SECURITY.md](SECURITY.md). Report privately; do not open public issues.
