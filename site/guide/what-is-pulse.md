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
operator ──ssh -L──▶ 127.0.0.1:8080 dashboard ───────▶ └───────────────────────────────────────────┘
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
- **a public dashboard.** The dashboard has no login yet and is reachable only through an SSH tunnel. Single sign-on
  (OIDC) is planned, not implemented.
- **encrypted at rest inside NATS.** Disk encryption is the host's job. Backups are encrypted.

## Status

**Pre-release.** No version has been tagged yet. All milestones up to the SDKs are implemented and tested locally;
the reference deployment (Ansible on a Debian 13 VPS, Cloudflare via Terraform) is tested in containers but not yet
verified on a real server. The sizing spike that sets the subject-cardinality guideline has not been run. Read the
[security model](/security/) before you rely on it.

Next: [Concepts](/guide/concepts).
