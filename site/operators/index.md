# Operator guides

Task-oriented guides for the person who runs Pulse. Pulse is a single-owner system: one operator, one VPS, one
`pulsed` and one NATS server. The detailed contract lives in the repository's
[`docs/operator/`](https://github.com/aurimasniekis/go-pulse/tree/main/docs/operator); these pages explain how to use it.

<Shot name="overview" themed alt="Pulse overview: event counts, throughput, top subjects and the disk forecast" caption="The Overview page" />

## Reaching the dashboard

The dashboard and the admin API have **no login** (`PULSE_AUTH_MODE=none`). They listen on host loopback only, so you
reach them through an SSH tunnel:

```sh
ssh -L 8080:127.0.0.1:8080 you@vps.example.com
# then open http://127.0.0.1:8080
```

The same tunnel serves the admin commands of the `pulse` CLI (`pulse clients`, `pulse streams`, `pulse backup`), which
default to `--admin-url http://127.0.0.1:8080`. Single sign-on (OIDC) is planned but not implemented: pulsed refuses to
start with `PULSE_AUTH_MODE=oidc`, and the prod profile refuses to expose the admin listener while auth is `none`.

Everyone with a shell on the VPS can reach the loopback port. That is accepted: on a single-user VPS, a local user is
already the operator ([security model](/security/)).

## Everyday work

| Task | Guide |
|---|---|
| Give a tool its own credentials and exactly the subjects it needs | [Clients and grants](/operators/clients) |
| HTTP tokens for scripts and services that cannot speak NATS | [Clients and grants](/operators/clients#http-tokens) |
| Look at events, streams, consumers, KV buckets and objects | [Streams, KV and objects](/operators/streams) |
| See who changed what | [Clients and grants](/operators/clients#audit-log) |
| Receive webhooks from GitHub or Stripe, send events to your services | [Webhooks in and out](/clients/webhooks) |

## Setup and operations

| Task | Guide |
|---|---|
| Hostnames, Cloudflare, the origin certificate and Let's Encrypt for NATS | [Cloudflare and TLS](/operators/tls) |
| The nftables table, automatic bans, lifting a ban | [Firewall and bans](/operators/bans) |
| The master key, the key store, what to keep offline | [Keys and credentials](/operators/keys) |
| Nightly encrypted backups, restores, disaster recovery | [Backup and restore](/operators/backups) |
| Logs, metrics, alert rules, Telegram and Discord | [Monitoring and alerts](/operators/monitoring) |
| New releases, migrations, rollback | [Upgrades](/operators/upgrades) |

First time? Start with [Deploy on a VPS](/guide/deploy), then [Connect your first tool](/guide/first-client).

## Where state lives

| What | Where | Backed up |
|---|---|---|
| Events, user streams, KV buckets, object stores | NATS JetStream (`nats-data` volume) | Yes, as JetStream snapshots |
| Clients, grants, tokens, webhooks, bans, audit, alerts, rollups, settings | SQLite `pulse.db` (`pulsed-data` volume) | Yes |
| Signing keys, internal CA, ACME account | `keys.enc`, sealed with the master key | Yes (still sealed) |
| Master key | A compose secret from your vault, never on a volume | **No**: keep it in your vault |
| age backup identity | Offline only | **No**: keep two offline copies |
