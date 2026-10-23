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
- M5: rollups (minute/hour/day counts by prefix and type, exactly once), subject index, disk forecast; Overview and
  Subjects pages; admin publish endpoint and "Publish event" in the Explorer.
- M6: HTTP tokens and the public ingress API (publish, batch, request bridge, replay, SSE tail); client, token and
  audit admin APIs; Clients, HTTP tokens and Audit pages with show-once secrets and connection snippets.
- M7: inbound webhooks (GitHub, Stripe, HMAC, shared secret) and outbound webhooks (ordered, retried, signed,
  SSRF-guarded) with a delivery log; Webhooks page.
- M8: automatic bans (NATS, HTTP, webhooks) enforced over HTTP, in nftables (`pulsed banagent`) and optionally in a
  Cloudflare IP list; origin TLS with hot reload and Authenticated Origin Pulls; Cloudflare-aware client IP; `/ws`
  proxy; Security page. Prod now requires `PULSE_AOP_CA_FILE` for the public listener.
