# Changelog

All notable changes are documented here. Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning: [docs/VERSIONING.md](docs/VERSIONING.md).

## [Unreleased]

### Added
- M0 skeleton: product contract, plan, decisions, threat model, test strategy; `pulsed version|healthcheck`; embedded
  dashboard shell (Mantine 8, TanStack Router); distroless image; CI and release workflows.
- M1: `pulsed init` (sealed key store, operator mode `nats.conf`, bootstrap TLS, banagent token), `pulsed serve`
  (SQLite state, SYS/APP connections, APP account sync), client identities with per-client scoped signing keys,
  `pulsed keys export`, dev compose stack, reference prod compose.
- M2: envelope package and SDK conformance vectors, `EVENTS` and `blobs` ensured at start, Go SDK (`pkg/pulse`),
  `pulse context|pub|sub`, sizing spike tooling (`make spike`, operator-triggered).
- M3: admin API (system, streams, consumers, explorer, SSE tail) and public listener skeleton on Huma v2, Host
  allowlist and CSRF header on the admin listener, `pulsed openapi`, generated TypeScript client types.
- M4: dashboard Explorer, Live tail and Streams & consumers pages, NATS status in the header, Playwright smoke tests.
