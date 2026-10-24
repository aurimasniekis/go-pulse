# Troubleshooting

Start with three places: `docker compose logs pulsed` (JSON logs), `curl -fsS 127.0.0.1:9464/readyz` on the server,
and the dashboard's **System**, **Security** and **Alerts** pages.

## Tools and clients

| Symptom | Check | Fix |
|---|---|---|
| `Authorization Violation` on connect | The client's status and expiry on the Clients page | Expired or revoked credentials: rotate the client and deploy the new `.creds`. |
| Publish or subscribe **times out** after ~5 s | The client's grants | NATS reports permission violations asynchronously, so a denied JetStream call looks like a timeout. Add the grant; the tool reconnects with it, no new `.creds` needed. |
| `Permissions Violation for … _INBOX.…` | The tool sets its own inbox prefix | Use an SDK, or `--inbox-prefix _INBOX_<client-id>` with the `nats` CLI. |
| `Permissions Violation for … CONSUMER.CREATE …` | Subscribe and durable grants | The filter must be inside a `subscribe` grant; durable names must match a `durable` grant. Library helpers that create consumers differently are refused; use the SDKs. |
| `invalid event subject` | The subject | At least four lowercase tokens starting with `evt`: see [the envelope](/clients/envelope). |
| `x509: certificate signed by unknown authority` on 4222 | System → Certificates | Until Let's Encrypt succeeds, NATS serves the internal bootstrap certificate: pass the internal CA (`--ca`, `WithCAFile`, `ca=`). ACME errors are on the System page. |
| Connection to 4222 or 443 hangs | Security page: is the address banned? | Lift the ban. The admin tunnel comes from loopback and is never banned, so the dashboard always works. |
| A durable replays from an old position | Durable start options only apply when the consumer is created | Delete the consumer on the Streams page (or use a new durable name) to start over. |
| Live tail shows "sampled out" | 200 events/s per tail session | Expected under load; the Explorer and replay have every stored event. |

## HTTP and webhooks

| Symptom | Check | Fix |
|---|---|---|
| `401` from `/api/v1` | Token prefix, expiry, revocation and networks on the HTTP tokens page | Use the full `pulse_<prefix>_<secret>` token; check the source network list. Repeated 401s ban the address. |
| `403` from `/api/v1` | The grant for that subject, or a ban | Add the grant, or lift the ban on the Security page. |
| `429` | 20 requests/s per address | Back off for `Retry-After`; batch publishes (up to 100 per request). |
| Inbound webhook gets `401` | The hook's verifier and secret; the sender's signature header | Paste the provider's secret again (Stripe's `whsec_…`), or rotate and update the sender. |
| Inbound webhook gets `404` | Hook name and enabled flag | The URL is `/hooks/<name>` exactly. |
| Requests never reach `pulsed` | Cloudflare WAF events; origin firewall | Only Cloudflare may reach 443, and only with Authenticated Origin Pulls. Direct requests to the server's IP are dropped by design. |
| Outbound deliveries stuck on one event | Delivery log for that hook | Delivery is in order: fix the endpoint, or wait until the event is marked dead (about two hours), or pause the hook. |
| Outbound delivery error "destination address is not allowed" | The URL resolves to an internal address | Allowlist it with `PULSE_OUTBOUND_ALLOW`. |

## Dashboard sign-in

| Symptom | Check | Fix |
|---|---|---|
| The sign-in page says no administrator exists | — | Run `docker compose exec pulsed pulsed admin bootstrap` (dev: `make dev-admin`) and open the link within 30 minutes. |
| "invalid credentials" | Email and password; the audit log (`user.login`) | The same message covers unknown emails, wrong passwords and disabled users. An administrator can issue a password link on the Users page. |
| "too many attempts; try again later" | Sign-in throttling | Wait (at most 15 minutes) or have an administrator clear the throttle on the user's page. |
| Locked out: lost TOTP and recovery codes, SSO down while password sign-in is off | — | Break-glass on the server: `pulsed admin recover --email you@example.com` ([details](/operators/users#break-glass)). |
| SSO returns to the sign-in page with an error | The provider's redirect URI, the issuer, a **Test login** on the provider page | Register exactly `<PULSE_ADMIN_URL>/auth/oidc/callback`; the issuer must match the discovery document exactly. "Not provisioned" means the identity is not linked and the provider is not JIT. |
| The dashboard keeps asking to confirm it is you | `PULSE_REAUTH_WINDOW` (5 min) | Expected for secrets, deletes, restores and user or SSO changes; confirm and the action continues. |
| Signed out after a while | `PULSE_SESSION_IDLE` (30 min), `PULSE_SESSION_MAX` (12 h) | Expected. Sessions are also tied to the listener: a tunnel session does not work on the public dashboard. |
| `pulse clients …` asks for a token, or 401 | The token, the listener | Create a personal access token on the Account page and pass `--admin-token-file` or `PULSE_ADMIN_TOKEN`. Tokens work only through the tunnel (`http://127.0.0.1:8080`), not on the public dashboard. |
| `pulse clients create` gets 403 | The token's role | The token needs the `operator` or `administrator` role, and you must still hold it. |

## Server

| Symptom | Check | Fix |
|---|---|---|
| `pulsed` stays unhealthy | `docker compose logs pulsed`; `curl 127.0.0.1:9464/readyz` | Usually NATS is not reachable or pushing the APP account failed: check `nats` health and `PULSE_NATS_URL`. |
| `pulsed` refuses to start in production | The error names the setting | The production profile refuses unsafe combinations: plain HTTP on the public listener, no AOP CA, a non-loopback admin listener, a reauth window over 15 minutes, an `http://` dashboard URL, the removed `PULSE_AUTH_MODE`, the test DNS provider. Fix the configuration; do not switch to the dev profile. |
| Dashboard says the UI is not built | A local build without the UI | Use the release image, or `make build` (which runs `make web` first). |
| Dashboard answers `421 Misdirected Request` | The `Host` header | The admin listener accepts only `127.0.0.1`, `localhost`, `::1` and `PULSE_ADMIN_HOSTS`. Open it as `http://127.0.0.1:8080` through the tunnel. |
| Alerts fire but no messages arrive | Alerts → Notifiers (sent, failed, suppressed, last error) | Fix the bot token, chat ID or webhook file; use **Test**. Rate-limited messages count as suppressed. |
| Backups fail | Backups page error; `pulse backup ls` | S3 endpoint, credentials and bucket. The error names the failing step. |
| Disk filling up | Overview forecast; `disk_forecast` and `stream_usage` alerts | Add disk, or accept that `EVENTS` discards its oldest events at `max_bytes`. pulsed never shrinks `EVENTS`: a lower `PULSE_EVENTS_MAX_BYTES` is ignored with a warning. |
| Docker networking breaks after restarting nftables | The `nftables.service` override | Debian's unit flushes the whole ruleset on stop. Install the drop-in that clears `ExecStop`, then restart Docker once. See [Firewall and bans](/operators/bans). |
| Bans have no effect on NATS | `banagent` logs; `nft list set inet pulse_edge pulse_ban_v4` | `banagent` needs host networking with `NET_ADMIN` and runs as root inside its container (added capabilities do not reach the image's non-root user). |

Still stuck? Open an issue on [GitHub](https://github.com/aurimasniekis/go-pulse/issues) with the `pulsed version`
output and the relevant log lines, after removing tokens, `.creds` content and addresses you do not want public.
Report security problems privately (see the [security model](/security/)).
