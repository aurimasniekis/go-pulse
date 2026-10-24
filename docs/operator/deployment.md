# Deployment

The reference topology is one VPS behind Cloudflare. [`deploy/examples/compose.yaml`](../../deploy/examples/compose.yaml)
is the reference compose file; the deployment repository renders its own copy and pins the `pulsed` image by digest.

## Services

| Service | Role | Network | Notes |
|---|---|---|---|
| `init` | `pulsed init`: key set, operator JWT, `nats.conf`, bootstrap TLS leaf, banagent token. Idempotent. | none | Runs to completion before `nats`. |
| `nats` | NATS 2.15 with JetStream, configured by `nats.conf` from init | `pulse` bridge; 4222 public | Monitoring (8222) and WebSocket (8080) stay inside the compose network. |
| `pulsed` | `pulsed serve`: admin UI/API, public API, hooks, `/ws` proxy, rollups, bans, alerts, ACME, backups | `pulse` bridge | 443 → 8443 (Cloudflare only); admin 8080 and metrics 9464 on host loopback only. |
| `banagent` | `pulsed banagent`: keeps the nftables ban sets in sync with pulsed | host, `NET_ADMIN` | Reads the ban feed from `127.0.0.1:9464`. |
| `alloy` | Scrapes pulsed and the host ([`config.alloy`](../../deploy/examples/config.alloy)) | host | No remote_write by default. |

Start order: `init` (completed) → `nats` (healthy) → `pulsed` (healthy) → `banagent`, `alloy`. Memory limits match
[SIZING.md](../SIZING.md).

## Host prerequisites

- Docker with `userland-proxy: false` and `ip6tables: true`, so NATS sees real client IPs on IPv4 and IPv6.
- The nftables table of [firewall.md](firewall.md): 443 is accepted only from Cloudflare, and the ban sets are dropped
  at `prerouting priority raw`.
- `/srv/pulse/secrets/` owned by uid 65532, mode 0400 per file: `master.key`, the Cloudflare origin cert and key, the
  AOP CA, and the optional tokens (ACME DNS, Cloudflare ban list, Telegram, Discord, S3 keys).
- DNS: the public hostname is proxied (orange cloud); the raw NATS hostname is DNS-only (grey cloud) and points at the VPS.

## First deploy

1. Generate the master key once and keep it in the vault: `openssl rand -hex 32`.
   Losing it makes `keys.enc` unreadable ([keys.md](keys.md)).
2. Create the age backup identity offline: `pulsed backup keygen > identity.txt`. Only the printed recipient
   (`age1…`) goes on the server ([backups.md](backups.md)).
3. Render the compose file and secrets, then `docker compose up -d`. `init` creates everything under `/data`.
4. Check: `docker compose ps` (all healthy); `curl -fsS 127.0.0.1:9464/readyz`.
5. Create the first administrator: `docker compose exec pulsed pulsed admin bootstrap` prints a one-time setup link
   ([users.md](users.md)). Open it through a tunnel (`ssh -L 8080:127.0.0.1:8080 <vps>`) or on the public dashboard host,
   set a password and enroll TOTP. The System page shows the certificates, NATS and JetStream; Alerts shows the built-in rules.

## Day-to-day access

- The dashboard needs a signed-in user ([users.md](users.md)). It is always reachable through the SSH tunnel and, with
  `PULSE_ADMIN_PUBLIC=true`, on its hostname behind Cloudflare. `pulse clients`, `pulse streams` and `pulse backup` use
  the tunnel with a personal access token (`--admin-url`, `--admin-token-file`).
- Tools connect to `tls://<raw host>:4222` with their `.creds`, or call the public HTTP API with a token.
