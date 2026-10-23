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
