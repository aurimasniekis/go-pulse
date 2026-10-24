# Security model

This page summarizes what Pulse protects, what it guarantees and what it does not. The canonical texts are the
[product contract](https://github.com/aurimasniekis/go-pulse/blob/main/docs/GOAL.md) (§4) and the
[threat model](https://github.com/aurimasniekis/go-pulse/blob/main/docs/THREAT_MODEL.md). If they disagree with this
page, they win.

## Read this first

**pulsed is a high-value component.** Whoever controls the pulsed process and holds the master key can mint any
credential and read every event. Payloads are stored **in plaintext inside NATS**: root on the VPS can read the whole
history. Backups are encrypted; the live data is protected only by the host.

Pulse is built for one owner on one VPS. Run it on a host you keep patched, with SSH keys only and full-disk encryption
where your provider offers it, and keep the master key and the backup identity off the server.

## What Pulse protects

| Asset | Where |
|---|---|
| Event history (years of personal data) | NATS `EVENTS`, backups |
| Operator and account signing keys | `keys.enc`, sealed with the master key |
| The master key | Your vault, a compose secret |
| Client `.creds` | Each tool's host |
| HTTP tokens, webhook secrets, notifier tokens | SQLite (hashes or sealed values), secret files |
| pulsed state: clients, grants, audit, bans | SQLite `pulse.db` |
| Dashboard users: password hashes, TOTP secrets, sessions, SSO client secrets | SQLite `pulse.db` (Argon2id hashes; secrets sealed and tokens MACed with keys derived from the master key) |
| TLS keys: origin certificate, Let's Encrypt, internal CA | Data volume, secrets |
| Backups | S3, encrypted with age |
| Cloudflare and S3 API tokens | Secret files |

Against: internet attackers on 4222 and 443, direct-to-origin traffic that bypasses Cloudflare, other Cloudflare
customers, a compromised tool holding one client's credentials, forged webhooks, outbound webhooks steered at internal
addresses, the backup storage provider, password guessing and stolen sessions on the dashboard, a misbehaving
identity provider, and unprivileged local processes.

## What Pulse guarantees

These hold when Pulse is deployed as the operator guides describe. Status words are strict: **implemented** means the
code exists, **tested** means automated tests in the repository pass, **externally verified** means it was checked on a
real deployment or by someone outside the implementing work. **No guarantee is externally verified yet.**

| # | Guarantee | Status |
|---|---|---|
| G-1 | A tool client can publish or subscribe only on subjects its grants allow. A new client has no permissions. | tested (embedded NATS) |
| G-2 | A revoked client is disconnected within 5 seconds and cannot reconnect with the same credentials. | tested |
| G-3 | Client seeds are shown once and never stored by pulsed. | tested |
| G-4 | Every admin UI and API request is made by a signed-in user and allowed only by that user's role; sign-in failures are throttled and audited. | tested (every operation mapped to a permission, role, CSRF, Origin, session, TOTP and SSO tests; SSO against an in-process identity provider only) |
| G-5 | In the prod profile, public HTTP routes accept only connections that passed Cloudflare Authenticated Origin Pulls. | tested locally with a self-made CA |
| G-6 | The real client IP comes from `CF-Connecting-IP` only when the peer is in Cloudflare's ranges and passed mTLS. | tested |
| G-7 | Inbound webhooks are accepted only with a valid signature or secret for that hook. | tested (GitHub's documented vector; Stripe against its documented scheme) |
| G-8 | Outbound webhooks cannot reach loopback, private, link-local or metadata addresses. | tested |
| G-9 | Signing keys at rest are sealed with a master key that never touches the data volume. | tested |
| G-10 | Backups are encrypted before they leave the host. | tested |
| G-11 | Repeated authentication failures from one address lead to a ban, enforced over HTTP and in nftables. | tested (nftables in a privileged container and a Debian 13 converge test; not yet on a real VM) |

## Controls by threat

| Threat | Controls |
|---|---|
| Credential guessing on 4222 | NATS JWT authentication (no passwords); auth errors feed the ban detector; nftables drop |
| Token guessing on the HTTP API | 256-bit tokens stored hashed; 401s feed the ban detector; per-IP rate limit (20 req/s, burst 40); a Cloudflare rate-limit rule |
| Traffic straight to the origin, bypassing Cloudflare | nftables accepts 443 only from Cloudflare's ranges; mTLS (AOP) is required in prod |
| Another Cloudflare customer pointing a zone at the origin | Per-hostname AOP with your own CA, not Cloudflare's shared one |
| Spoofed `CF-Connecting-IP` or `X-Forwarded-For` | Trusted only from Cloudflare peers that passed mTLS; the `/ws` proxy replaces `X-Forwarded-For` with exactly one trusted value (NATS trusts the first) |
| Unauthorized dashboard use | Sign-in for every request (no anonymous mode); every operation needs a permission from the user's role; server-side sessions bound to the listener that created them; the session's CSRF token and a same-origin `Origin` on every write; a Host allowlist against DNS rebinding (421 otherwise); strict CSP, no CORS; on the internet only behind Cloudflare and AOP |
| Password guessing on the dashboard | Argon2id; one answer for every failure after the same work; throttling per account and per address (up to 15 min); TOTP mandatory for administrators and operators; failed public sign-ins feed the ban detector; audit |
| A stolen session cookie | `HttpOnly` `__Host-` cookie; strict CSP; 30-minute idle and 12-hour absolute expiry; re-authentication within 5 minutes for secrets, deletes, restores and user or SSO changes; session list and revocation |
| A forged or replayed SSO login | PKCE, single-use state bound to the browser, nonce; ID token signature (RS256, ES256, EdDSA only), issuer, audience, `azp` and time checks; users matched by issuer and subject, never email; JIT provisioning only with restrictions |
| A leaked CLI token | Personal access tokens work only on the loopback listener, carry one role capped by the user's current roles, expire and are revocable |
| Every administrator locked out | Last-administrator guards; SSO-only only after a test login with an SSO administrator; break-glass `pulsed admin recover` on the host |
| A compromised tool trying to do more | Least-privilege grants, fixed denies (stream admin, `$SYS`, `$KV`, unfiltered direct get), a per-client inbox prefix |
| A compromised tool keeping access | Revoke: revocation list, account push and kick within 5 seconds |
| Forged or replayed webhooks | Per-hook verifiers, constant-time comparison, timestamp tolerance, bans |
| SSRF through outbound webhooks | The dialer checks every resolved address at connect time (so DNS rebinding is covered) and refuses loopback, private, link-local, CGNAT, multicast and metadata ranges unless allowlisted |
| Durable consumer name squatting | Durables only as `<client-id>_<name>` through an explicit grant |
| Key theft from the data volume | `keys.enc` sealed (XChaCha20-Poly1305) with the master key, which is only a compose secret |
| Backup disclosure | age encryption before upload; the identity stays offline; chunk names are keyed hashes |
| Forged backup objects | Chunk names, the manifest MAC and file hashes are keyed from the master key and checked before use |
| Disk exhaustion | `max_bytes` with discard old, 1 MiB `max_payload`, rate limits, disk forecast alerts |
| Expensive explorer queries | Budgets per page (50k messages, 64 MB, 3 s), 4 concurrent queries, capped tail sessions |
| Secrets in logs | Redaction; the audit log never stores secrets; URLs are stripped from outbound errors |
| Banning yourself or Cloudflare | A never-ban list: Cloudflare's ranges, private ranges, the host's own addresses, an allowlist |

Pulse uses only the Go standard library, `golang.org/x/crypto` and the NATS project's `nkeys` and `jwt` packages for
cryptography. TLS verification is never disabled.

## What Pulse does not guarantee

- **Protection from the host.** Root on the VPS can read events, `pulse.db` and, with the compose secret, the keys.
- **Confidentiality from Cloudflare.** Cloudflare terminates TLS for the public HTTP routes and sees those requests in
  plaintext, and it is trusted to set `CF-Connecting-IP` honestly.
- **Fine-grained read access.** Grants are subject based. A client allowed to read `evt.home.>` reads every event on
  those subjects, payloads included.
- **DDoS protection.** Bans are a nuisance filter against credential guessing, bounded by nftables set sizes and
  Cloudflare's free-plan limits.
- **Exactly-once delivery** beyond JetStream's 2-minute de-duplication window.
- **Replay protection for shared-secret webhooks.** Use a signing verifier whenever the sender supports one.
- **Consumer isolation between clients on the same stream.** A `subscribe` grant lets a client pull from any consumer on that stream whose name it knows, including another client's durable with a wider filter, so it could read events outside its own grants. Durable names start with the owning client's ID, an 80-bit random ULID that other clients are never shown, so this needs a leaked client ID. A fix that scopes
  pulls to the client's own consumers is open (R-05 in DECISIONS.md).
- **Protection from your identity provider.** A provider (or whoever controls it) can sign in any identity linked to
  it, with the roles its group mappings grant.
- **A stolen session within the reauth window.** A session cookie stolen from your browser can do what your role
  allows until it expires, except the actions that ask you to re-authenticate after 5 minutes.
- **High availability.** One NATS server and one pulsed. Recovery is a restore from backup.
- **Backups against deletion** by someone holding the server's S3 key. Use versioned or object-locked buckets.
- **Recovery without the master key and the age identity.** Losing either is final, by design.

## Release status

Pulse has **no tagged release yet**. The code is complete through the planned milestones and tested locally; these
items are still open:

- **No external review.** Nobody outside the implementing work has reviewed the code, the key handling or the backup
  format.
- **Not yet verified on a real VPS.** The firewall, ban enforcement, IPv6 client addresses and the host roles were
  tested in containers (including a Debian 13 converge test) but not on a real VM behind Cloudflare.
- **Sizing not measured.** The memory budget (2 vCPU, 2 GB RAM) and the subject-cardinality guideline are provisional:
  the sizing spike (about 10 GB of events, 5,000 subjects) has not been run.
- **CI on GitHub** runs only since the repository was published; the release workflow has not produced a release yet.
- **Webhook verifiers** are tested against documented vectors and schemes, not live GitHub or Stripe deliveries.
- **Single sign-on** is tested against an in-process identity provider (discovery, PKCE, JWKS rotation, token
  validation, step-up, linking), not yet against a live provider such as Keycloak or Entra ID.
- **No visual regression tests** for the dashboard; browser tests run in Chromium only.
- **Consumer scoping (R-05).** A `subscribe` grant can pull from another client's consumer on the same stream if it
  knows the consumer's name (see the limits above).
- **`EVENTS` cannot be shrunk (R-06).** A lower `PULSE_EVENTS_MAX_BYTES` is ignored with a warning.
- The prod-profile refusals do not yet cover every unsafe combination, and the refusal to put the database on a
  network filesystem is implemented but untested.

## Reporting vulnerabilities

Report vulnerabilities privately, never in a public issue. See
[SECURITY.md](https://github.com/aurimasniekis/go-pulse/blob/main/SECURITY.md).
