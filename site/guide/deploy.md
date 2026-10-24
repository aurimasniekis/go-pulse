# Deploy on a VPS

The reference deployment is **one small VPS behind Cloudflare**: 2 vCPU, 2 GB RAM, 50–100 GB disk, Debian 13, Docker.
This page describes what such a deployment needs and how the first start goes. The operator guides go deeper into each
part.

::: warning No release yet
No version has been tagged, so there is no published image yet. Releases will push
`ghcr.io/aurimasniekis/pulsed:<version>` for linux/amd64 and linux/arm64, with an SBOM and build provenance, which you
verify with:

```sh
gh attestation verify oci://ghcr.io/aurimasniekis/pulsed:0.1.0 --repo aurimasniekis/go-pulse
```

Until then, build the image yourself (`make image` builds `pulsed:local` for your platform; set `IMAGE=` to change the
tag) and load it on the server with `docker save pulsed:local | ssh <vps> docker load`. Pin images by digest in
production, never by a moving tag.
:::

## Topology

<Flow />

| Service | Role | Network and ports |
|---|---|---|
| `init` | One-shot `pulsed init`: key set, operator JWT, `nats.conf`, bootstrap TLS certificate, banagent token. Idempotent; runs on every start. | none |
| `nats` | NATS 2.15 with JetStream, configured by `init` | `4222` public (IPv4 and IPv6). Monitoring and WebSocket stay inside the compose network. |
| `pulsed` | `pulsed serve`: dashboard, admin and public APIs, webhooks, `/ws`, rollups, bans, alerts, ACME, backups | `443 → 8443` public but Cloudflare-only; `127.0.0.1:8080` dashboard; `127.0.0.1:9464` metrics |
| `banagent` | Mirrors bans into nftables sets | host network, `NET_ADMIN` |
| `alloy` | Scrapes `pulsed` and host metrics (no remote write configured) | host network |

Start order: `init` (completed) → `nats` (healthy) → `pulsed` (healthy) → `banagent`, `alloy`. The reference compose file
is [`deploy/examples/compose.yaml`](https://github.com/aurimasniekis/go-pulse/blob/main/deploy/examples/compose.yaml),
with memory limits that fit the 2 GB box (NATS 900 MiB, `pulsed` 448 MiB).

## DNS and Cloudflare

| Hostname | Cloudflare | Serves |
|---|---|---|
| `events.example.com` | **proxied** (orange cloud) | `https://…/api/v1`, `/hooks/<name>`, `wss://…/ws` on 443 |
| `r.events.example.com` | **DNS only** (grey cloud) | raw NATS TLS on 4222, with a Let's Encrypt certificate `pulsed` obtains itself |

On the Cloudflare side the public hostname needs:

- an **Origin CA certificate** for `pulsed`'s public listener;
- **per-hostname Authenticated Origin Pulls** with your own client CA, not Cloudflare's shared one, so other
  Cloudflare customers cannot reach your origin;
- optionally WAF and rate-limit rules, and an IP list that mirrors long bans to the edge;
- a scoped API token for ACME DNS-01 on the raw hostname (Zone DNS edit), and one for the IP list if you use it.

The production profile refuses to start the public listener without the origin certificate and the AOP CA. See
[Cloudflare and TLS](/operators/tls).

## Host prerequisites

- **Docker** with `userland-proxy: false` and `ip6tables: true` in `daemon.json`, so NATS sees real client addresses
  on IPv4 and IPv6 (bans depend on it).
- **nftables** with the `inet pulse_edge` table: 443 accepted only from Cloudflare's ranges, and the two ban sets
  dropped at `prerouting priority raw`, before Docker's NAT. Use `destroy table`, never `flush ruleset`, and override
  Debian's `nftables.service`, whose stop action flushes Docker's rules too. See [Firewall and bans](/operators/bans).
- SSH with keys only, and fail2ban for SSH (`pulsed` does not protect SSH).
- A swapfile (2 GB, `swappiness=10`) on a 2 GB box.
- **Secrets** in `/srv/pulse/secrets/`, owned by uid 65532, mode 0400 per file, mounted as compose secrets:

| File | Content |
|---|---|
| `master.key` | 32 random bytes, hex or base64 (`openssl rand -hex 32`). Keep a copy in your vault. |
| `cf_origin.crt`, `cf_origin.key` | Cloudflare Origin CA certificate and key |
| `aop_ca.crt` | The client CA for Authenticated Origin Pulls |
| `acme_cf_token` | Cloudflare token for DNS-01 on the raw hostname |
| optional | Cloudflare IP-list token, Telegram bot token, Discord webhook URL, S3 access and secret keys |

Every `PULSE_*` variable and secret file is listed in the [configuration reference](/reference/configuration).

## First start

1. Generate the master key and store it in your vault.
2. Create the backup identity **on your own machine**, not the server:

   ```sh
   docker run --rm ghcr.io/aurimasniekis/pulsed:<version> backup keygen > backup-identity.txt
   ```

   Until a release exists: `docker run --rm pulsed:local backup keygen > backup-identity.txt`, after `make image`.
   Keep `backup-identity.txt` offline. Only the printed recipient (`age1…`) goes into the server's configuration
   (`PULSE_BACKUP_AGE_RECIPIENTS`). See [Backup and restore](/operators/backups).
3. Render the compose file from the reference: set `PULSE_PUBLIC_HOST`, `PULSE_RAW_HOST`, `PULSE_HOST_IPS` (the VPS's
   own addresses, never banned), `PULSE_EVENTS_MAX_BYTES` (about 60% of the data disk), the image digest, and the
   backup and notifier settings you want.
4. Start it:

   ```sh
   cd /srv/pulse && docker compose up -d
   docker compose ps                        # all healthy
   curl -fsS 127.0.0.1:9464/readyz
   ```

5. Create the first dashboard administrator. There are no default credentials:

   ```sh
   docker compose exec pulsed pulsed admin bootstrap   # prints a one-time setup link (30 minutes)
   ```

6. Open the dashboard through a tunnel from your machine:

   ```sh
   ssh -L 8080:127.0.0.1:8080 <vps>
   ```

   Then open the setup link (it points at `http://127.0.0.1:8080` unless you set `PULSE_ADMIN_URL`), choose an email
   and password, sign in and enroll TOTP. See [Users and single sign-on](/operators/users). The System page shows
   NATS, JetStream and every certificate. Until the
   Let's Encrypt certificate for `r.events.example.com` is issued (with `PULSE_ACME_ENABLED=true`; progress and errors
   are on the System page), tools must trust the internal CA (**System → Connect → Internal CA**).

## Optional: the dashboard on the internet

The dashboard can also be served on the public listener, behind Cloudflare and Authenticated Origin Pulls, so you do
not need the tunnel. Set `PULSE_ADMIN_PUBLIC=true` and pick a hostname:

- **its own hostname** (recommended): `PULSE_ADMIN_PUBLIC_HOST=admin.events.example.com`. Add a proxied (orange) DNS
  record for it and include the name on the Cloudflare origin certificate. That host serves only the dashboard, and
  `events.example.com` keeps serving only the API, webhooks and `/ws`;
- **the API hostname**: leave `PULSE_ADMIN_PUBLIC_HOST` unset; `events.example.com` then serves the dashboard on every
  path except `/api/v1`, `/hooks`, `/ws` and `/healthz`.

`PULSE_ADMIN_URL` becomes `https://<that host>`: run `pulsed admin bootstrap` after setting it so the link points
there. Failed sign-ins on the public dashboard count toward bans. Single sign-on providers redirect to
`<PULSE_ADMIN_URL>/auth/oidc/callback`.

## Day to day

- The dashboard needs a signed-in user. `pulse clients`, `pulse streams` and `pulse backup` use the tunnel
  (`--admin-url`, default `http://127.0.0.1:8080`) with a personal access token from your Account page
  (`--admin-token-file` or `PULSE_ADMIN_TOKEN`).
- Tools connect to `tls://r.events.example.com:4222` with their own `.creds`, or call
  `https://events.example.com/api/v1` with a token.
- Upgrades: take a backup, pin the new digest, `docker compose up -d`. See [Upgrades](/operators/upgrades).

Next: [Connect your first tool](/guide/first-client).
