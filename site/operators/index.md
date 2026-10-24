# Operator guides

Task-oriented guides for the person who runs Pulse. Pulse is a single-owner system: one operator, one VPS, one
`pulsed` and one NATS server. The detailed contract lives in the repository's
[`docs/operator/`](https://github.com/aurimasniekis/go-pulse/tree/main/docs/operator); these pages explain how to use it.

<Shot name="overview" themed alt="Pulse overview: event counts, throughput, top subjects and the disk forecast" caption="The Overview page" />

## Reaching the dashboard

The dashboard and the admin API need a **signed-in user** with a role: administrator, operator, auditor or viewer.
People sign in with a password and TOTP, or with single sign-on. The first administrator comes from
`pulsed admin bootstrap`. See [Users and single sign-on](/operators/users).

The admin listener is on host loopback, so you can always reach it through an SSH tunnel:

```sh
ssh -L 8080:127.0.0.1:8080 you@vps.example.com
# then open http://127.0.0.1:8080
```

With `PULSE_ADMIN_PUBLIC=true` the dashboard is also served behind Cloudflare, on its own hostname or on the API
hostname. The admin commands of the `pulse` CLI (`pulse clients`, `pulse streams`, `pulse backup`) always use the tunnel
(`--admin-url http://127.0.0.1:8080`) with a personal access token.

## Everyday work

| Task | Guide |
|---|---|
| Invite people, assign roles, set up single sign-on | [Users and single sign-on](/operators/users) |
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
| Clients, grants, tokens, webhooks, bans, audit, alerts, rollups, settings, dashboard users and SSO providers | SQLite `pulse.db` (`pulsed-data` volume) | Yes |
| Signing keys, internal CA, ACME account | `keys.enc`, sealed with the master key | Yes (still sealed) |
| Master key | A compose secret from your vault, never on a volume | **No**: keep it in your vault |
| age backup identity | Offline only | **No**: keep two offline copies |
