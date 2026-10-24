# Pulse documentation

The website at <https://aurimasniekis.github.io/go-pulse/> covers getting started, clients and the operator guides. The
files here are the detailed reference.

## Operators

| Document | Purpose |
|---|---|
| [operator/deployment.md](operator/deployment.md) | Topology, host prerequisites, first deploy, day-to-day access. |
| [operator/configuration.md](operator/configuration.md) | Every `PULSE_*` variable and the files a deployment provides. |
| [operator/users.md](operator/users.md) | Dashboard users, roles, TOTP, single sign-on, access tokens, break-glass. |
| [operator/keys.md](operator/keys.md) | Master key, `keys.enc`, client credentials, rotation, what to keep offline. |
| [operator/tls.md](operator/tls.md) | The three TLS surfaces and how each certificate is renewed. |
| [operator/firewall.md](operator/firewall.md) | The nftables tables and sets banagent relies on. |
| [operator/bans.md](operator/bans.md) | Detection, escalation, enforcement and lifting bans. |
| [operator/backups.md](operator/backups.md) | Running and checking backups, restores. |
| [operator/upgrades.md](operator/upgrades.md) | Releases, digests, migrations, rollback. |
| [operator/troubleshooting.md](operator/troubleshooting.md) | Symptoms and fixes. |
| [SIZING.md](SIZING.md) | The reference VPS, the sizing spike and the subject cardinality guideline. |

## Clients and formats

| Document | Purpose |
|---|---|
| [ENVELOPE.md](ENVELOPE.md) | Subjects and the event envelope every SDK reads and writes. |
| [API.md](API.md) | The public and admin HTTP APIs (OpenAPI 3.1 in `api/`). |
| [BACKUPS.md](BACKUPS.md) | Backup format, bucket layout and the restore procedure. |

## Project

| Document | Purpose |
|---|---|
| [VERSIONING.md](VERSIONING.md) | Versioning policy for the image, HTTP APIs, envelope, database, key store, backups and configuration. |
| [RELEASING.md](RELEASING.md) | How a release is cut and published. |

## Project planning (internal)

Internal design and status documents. They use requirement IDs (`PL-*`); nothing above depends on them.

| Document | Purpose |
|---|---|
| [GOAL.md](GOAL.md) | Product contract: requirements, guarantees, definition of done. |
| [PLAN.md](PLAN.md) | Architecture and design, milestone status and the requirement acceptance checklist. |
| [DECISIONS.md](DECISIONS.md) | Architectural decision records and open review items. |
| [THREAT_MODEL.md](THREAT_MODEL.md) | Assets, trust boundaries, threats, mitigations, residual risks. |
| [TEST_STRATEGY.md](TEST_STRATEGY.md) | Test layers, requirement→test matrix, performance method. |
