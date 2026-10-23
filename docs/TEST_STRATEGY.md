# Pulse — Test Strategy

> Status: established at M0 (2026-10-23).

## 1. Layers

| Code | Layer | Tooling | Runs in |
|---|---|---|---|
| U | Unit | `go test`, vitest | `make test`, `make web-test`, CI |
| I | Integration with an embedded NATS server booted from the generated `nats.conf` (`internal/testutil/natstest`) | `go test -race` | `make test`, CI |
| E | Browser E2E | Playwright against `make dev-up` | `make e2e`, CI e2e workflow |
| C | Static checks | golangci-lint, go vet, govulncheck, gitleaks, tsc | `make check`, CI |
| S | Spike / performance | `cmd/pulse-spike` (M2), recorded in SIZING.md | manual, recorded |
| R | Restore drill | Go test against SeaweedFS + embedded NATS (M11) | `make drill`, release gate |
| X | External verification | `events-wan-lt/scripts/vm-verify.sh`, real Cloudflare | manual, recorded in PROGRESS.md |

## 2. Obligations per area

| Area | Required layers |
|---|---|
| PL-KEY, PL-AUTH | U (generator tables, sealing, wrong key) + I (creds connect, AUTH.ERR, revoke → disconnect, restart persistence) |
| PL-BUS, PL-ENV | U (subject grammar, envelope round trip, conformance vectors) + I (EVENTS ensure, dedupe, replay by time) |
| PL-DB | U/I (pragmas on every connection, migrations, lock contention, prune) |
| PL-EXP | Table-driven I tests for each read mode, cursor tampering, budgets |
| PL-ROLL | I (exactly-once over crash/restart, dimension counts, forecast math U) |
| PL-EDGE | I (AOP with a locally generated CA and client cert, real-IP rules, XFF rewrite, route allowlist) + Docker ACME test against pebble/challtestsrv with a real nats-server (`make test-acme`) + X |
| PL-BAN | U (windows, escalation, never-ban) + I (advisory → ban) + Linux-only nft test (privileged container) + X |
| PL-HOOK | U (GitHub/Stripe doc vectors, HMAC, tolerance) + I (dispatcher retries against httptest, SSRF dialer) |
| PL-BAK | R (restore drill incl. timestamp preservation) |
| PL-UI | U (vitest) + E (Playwright smoke for every page) |
| PL-SDK | U (conformance) + I (interop e2e) |
| PL-OPS | C + image build + container smoke in CI |

## 3. Rules

- Tests generate their own keys, certificates and secrets at runtime. No secret fixtures.
- Integration tests use random ports and temp dirs; no reliance on Docker except where noted (`make e2e`, `make drill`, `make test-nft`, `make test-acme`).
- Every bug fix adds a regression test.
- Tests that issue `$SYS.REQ.SERVER.<id>.RELOAD` run a real `nats-server` subprocess built from the version pinned in
  `go.mod` (`natstest.Options{Process: true}`): the embedded server has an internal data race during config reload with
  the full resolver (nats-server 2.15.1, `diffOptions` vs. `DirJWTStore` expiry), which `-race` reports.
- `go test -race` must pass; goroutine leaks in long-running components are checked with `goleak` where feasible.
