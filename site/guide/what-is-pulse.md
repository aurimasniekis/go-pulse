# What is Pulse?

Pulse is a **personal, durable event bus**. Your tools, bots, agents and services publish events to it and consume
events from it. Every event is kept, so a tool you write next year can replay the last year of history from any point
in time.

Under the hood it is [NATS JetStream](https://docs.nats.io/nats-concepts/jetstream) plus `pulsed`, a Go server that
sets NATS up securely and runs everything around it: client credentials, an HTTP API, webhooks, bans, backups, alerts
and an operator dashboard.

```
tools / bots / agents ──NATS TLS (4222, own .creds)──▶ ┌───────────────── one VPS ─────────────────┐
browsers, HTTP tools ──HTTPS via Cloudflare (443)────▶ │ pulsed: API · dashboard · rollups · bans │
webhook senders ──HTTPS via Cloudflare──▶ /hooks/… ──▶ │ NATS 2.15 JetStream: EVENTS · KV · blobs │
operators ──password+TOTP or SSO──▶ dashboard ───────▶ └───────────────────────────────────────────┘
          via Cloudflare (optional) or ssh -L
```

<Shot name="overview" themed alt="Pulse overview: event counts, throughput chart, top domains and the disk forecast" caption="The Overview page" />

## The problem with ad-hoc glue

Personal automation tends to grow as point-to-point glue: a script that polls an API, a bot that calls another bot's
webhook, a cron job that writes to a file someone else reads. That glue:

- **forgets.** When a new tool arrives, the events it would have wanted are gone.
- **couples everything.** Each producer has to know each consumer, its URL and its credentials.
- **shares secrets.** One token for everything, copied into every tool.
- **is invisible.** Nobody can say what happened, when, and who sent it.

## What Pulse changes

| Without Pulse | With Pulse |
|---|---|
| Events disappear once delivered | Every event under `evt.>` is stored in the `EVENTS` stream, without a time limit, until the disk budget is reached. |
| A new tool starts with nothing | A tool replays from a timestamp (`pulse sub 'evt.home.>' --since 2026-01-01`), then keeps following live. |
| Producers know their consumers | Producers publish to a subject. Consumers subscribe to subject patterns. Neither knows the other. |
| One shared secret | Every tool gets its **own** NATS credential with **least-privilege grants**: no permissions until you add them. |
| Revocation means hunting for copies | Rotate or revoke one client in the dashboard. Its connections are cut within seconds. |
| Webhooks land in five different places | Inbound webhooks (GitHub, Stripe, HMAC) become events. Outbound webhooks push events to your services, signed and retried. |
| Nobody watches | An audit log, automatic bans for failed logins, Prometheus metrics, alerts to Telegram or Discord. |
| Backups are a someday task | Nightly, age-encrypted, deduplicated backups to S3 that keep sequence numbers and timestamps. |

## What Pulse is not

Pulse is deliberately small. It is not:

- **multi-tenant.** It has one owner. Clients are isolated by NATS permissions, not by tenants or accounts.
- **highly available.** One NATS server and one `pulsed` on one VPS. Recovery is restoring a backup.
- **a schema registry.** `ce-type` is a label (`home.door.opened.v1`), not a validated schema.
- **exactly once.** JetStream de-duplicates retries with the same event ID for two minutes. Beyond that, delivery is
  at least once; consumers should be idempotent.
- **an anonymous dashboard.** Every dashboard request needs a signed-in user (password with TOTP, or OIDC single
  sign-on). It is reachable through an SSH tunnel and, if you enable it, behind Cloudflare (on its own hostname or the
  API hostname).
- **encrypted at rest inside NATS.** Disk encryption is the host's job. Backups are encrypted.

## Status

**Release 0.1.2.** The server, the dashboard, the CLI and the Go, Python and TypeScript SDKs are
implemented and tested locally and in CI. The reference deployment (a Debian 13 VPS behind Cloudflare) is set up by a
separate Ansible and Terraform deployment, tested locally but not yet verified on a real server. The sizing spike that
sets the subject-cardinality guideline has not been run. Read the [security model](/security/) before you rely on it.

Next: [Concepts](/guide/concepts).
