# Pulse — Threat Model

> Status: established at M0 (2026-10-23). Update with every security-relevant ADR.
> Scope: the `pulsed` server, NATS as configured by pulsed, the SDKs/CLI, and the reference deployment (one VPS behind Cloudflare).

## 1. Assets

| # | Asset | Where | Why it matters |
|---|---|---|---|
| A1 | Event history | NATS `EVENTS` store, backups | Personal data from every tool, 1–2 years deep. |
| A2 | Operator and account signing keys | `keys.enc` (sealed) | Whoever holds them can mint any credential. |
| A3 | Master key | compose secret (ansible-vault) | Unseals A2. |
| A4 | Client credentials (`.creds`) | Each tool's host | Act as the client within its grants. |
| A5 | HTTP tokens, hook secrets, notifier tokens | SQLite (hashes / sealed values), secret files | Publish/forge events, spam alerts. |
| A6 | pulsed state | SQLite `pulse.db` | Client list, grants, audit, bans. |
| A7 | TLS keys (origin cert, LE cert, internal CA) | data volume, secrets | Impersonate the origin or the NATS endpoint. |
| A8 | Backups and age recipients | S3 | Copy of A1 and A6 (encrypted). |
| A9 | Cloudflare and S3 API tokens | secret files | DNS takeover, firewall bypass, backup deletion. |

## 2. Adversaries

| # | Adversary | Capabilities |
|---|---|---|
| ADV-1 | Internet attacker | Reaches 4222 and Cloudflare-fronted 443 routes; scans; brute-forces tokens and creds. |
| ADV-2 | Non-Cloudflare direct attacker | Sends traffic straight to the origin IP on 443. |
| ADV-3 | Compromised tool | Holds one client's `.creds` or one HTTP token. |
| ADV-4 | Malicious webhook sender | Posts forged or replayed payloads to `/hooks/*`. |
| ADV-5 | Malicious outbound target / DNS | Steers outbound webhooks to internal addresses (SSRF). |
| ADV-6 | Another Cloudflare customer | Points their own zone at the origin IP to bypass WAF rules. |
| ADV-7 | Backup storage provider | Reads or deletes S3 objects. |
| ADV-8 | Local unprivileged process on the VPS | Reaches loopback ports. |

## 3. Threats and controls

| ID | Threat | Adversary | Controls | Requirements | Residual risk |
|---|---|---|---|---|---|
| T-01 | Credential brute force on 4222 | ADV-1 | NATS JWT auth (no passwords), AUTH.ERR → ban detector → nftables drop | PL-AUTH-001, PL-BAN-001..007 | Attacker rotates IPs; bans are a nuisance filter. |
| T-02 | Token brute force on `/api/v1` | ADV-1 | 256-bit tokens, hashed at rest, 401 → ban, per-IP rate limit, CF rate-limit rule | PL-ING-001, PL-ING-007, PL-EDGE-009 | — |
| T-03 | Direct-to-origin traffic bypassing Cloudflare | ADV-2 | nftables allows 443 only from CF ranges; AOP mTLS required | PL-EDGE-003 | CF range list drift (refreshed daily). |
| T-04 | Another CF customer reaching the origin | ADV-6 | Per-hostname AOP with the module's own CA, not the shared CF CA | PL-EDGE-003, ADR-0009 | — |
| T-05 | Spoofed `CF-Connecting-IP` to evade bans or frame others | ADV-1/2 | Trusted only from CF peers that passed mTLS | PL-EDGE-004 | — |
| T-06 | Spoofed `X-Forwarded-For` on `/ws` (NATS trusts the first XFF) | ADV-1 | Proxy rewrites XFF to a single trusted value | PL-EDGE-005 | — |
| T-07 | Admin UI exposure (auth none) | ADV-1/8 | Loopback-only bind, prod refusals, CSRF header, strict CSP | PL-ADM-001..004, ADR-0008 | ADV-8 is admin. Accepted (single-user VPS). |
| T-08 | Compromised tool escalates beyond grants | ADV-3 | Least-privilege scoped signing key, explicit denies, inbox per client | PL-AUTH-003..006, ADR-0025 | Reads all events on granted subjects. |
| T-09 | Compromised tool keeps access after detection | ADV-3 | Revoke removes scoped key + revocation + KICK within 5 s | PL-AUTH-007 | — |
| T-10 | Forged/replayed webhooks | ADV-4 | Per-hook verifiers, constant-time compare, timestamp tolerance, bans | PL-HOOK-002..004 | Shared-secret hooks have no replay protection (documented). |
| T-11 | SSRF via outbound webhooks | ADV-5 | Dialer checks resolved IPs; blocks loopback/private/link-local/CGNAT/metadata; no redirects to blocked IPs | PL-HOOK-008 | DNS rebinding is handled by checking at dial time. |
| T-12 | Durable consumer name squatting | ADV-3 | Durables are created only with names prefixed by the client ID via an explicit `durable` grant; other names are ephemeral | ADR-0007 | A client with `*`-named consumer create on a filter it is allowed to read can attach to another consumer with an identical config on that filter; it gains nothing beyond its own grants but can steal deliveries. Accepted. |
| T-13 | Key theft from the data volume | ADV-8 | `keys.enc` sealed with the master key, which lives only in a compose secret | PL-KEY-001..002 | Root on the host can read both. |
| T-14 | Backup disclosure | ADV-7 | age encryption before upload; identities offline | PL-BAK-002 | Deletion by provider (use versioned buckets). |
| T-15 | Disk exhaustion / ingest flood | ADV-3 | `max_bytes` with discard old, `max_payload` 1 MB, per-IP rate limits, disk forecast alerts | PL-BUS-001, PL-OBS-004 | A granted publisher can push old history out. |
| T-16 | Explorer queries as a DoS vector | ADV-8 | Budgets (50k msgs, 64 MB, 3 s, 4 concurrent), tail session caps | PL-EXP-006..007 | — |
| T-17 | Log/audit leaks of secrets | all | Redaction helpers; audit never stores secrets | PL-OBS-001, PL-AUD-003 | — |
| T-18 | Cloudflare/DNS token misuse | ADV-1 after host compromise | Scoped tokens (DNS edit only for ACME, list edit only for bans) | PL-EDGE-006, PL-BAN-008 | — |
| T-19 | Ban of the operator or Cloudflare itself | ADV-1 (framing) | Never-ban list incl. CF ranges, private ranges, allowlist | PL-BAN-004 | — |
| T-20 | SQLite corruption or concurrent writers | — | flock, WAL + `synchronous=FULL`, `quick_check`, backups | PL-DB-001..007 | — |

## 4. Out of scope

Host compromise as root, Cloudflare compromise, NATS server vulnerabilities (tracked via releases), physical access.
