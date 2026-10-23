# Pulse

A personal, durable, secure event bus on NATS JetStream. Every tool publishes and consumes events through it, and any new
tool can replay years of history from a timestamp.

- `pulsed` — server: NATS bootstrap (JWT operator mode), public HTTP edge (tokens, webhooks, WebSocket), loopback admin
  dashboard, rollups, bans, backups, alerts. State in SQLite; events in JetStream.
- `pulse` — CLI: publish, subscribe, replay.
- SDKs — Go (`pkg/pulse`), Python (`sdk/python`), TypeScript (`sdk/ts`).

Status: under construction; see [docs/PLAN.md](docs/PLAN.md) §5.2 for milestone status.

## Documentation

| Doc | Purpose |
|---|---|
| [docs/GOAL.md](docs/GOAL.md) | Product contract and requirement IDs |
| [docs/PLAN.md](docs/PLAN.md) | Architecture, milestones, acceptance checklist |
| [docs/DECISIONS.md](docs/DECISIONS.md) | ADRs |
| [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) | Threats and controls |
| [docs/TEST_STRATEGY.md](docs/TEST_STRATEGY.md) | Test layers |
| [docs/operator/](docs/operator) | Configuration contract and operations |

## Development

```sh
make help        # list targets
make build       # UI + binaries into bin/
make check       # lint, vet, race tests, web tests, govulncheck, gitleaks
make dev-up      # local stack: init, nats, pulsed, SeaweedFS, pebble
make web-dev     # Vite dev server against the dev stack
```

Requires Go 1.27.3, Node 24 with pnpm 10, Docker.
