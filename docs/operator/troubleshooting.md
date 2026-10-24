# Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| `pulsed` stays unhealthy | `docker compose logs pulsed`; `curl 127.0.0.1:9464/readyz` | Usually NATS is not reachable or the APP account push failed: check `nats` health and `PULSE_NATS_URL`. |
| pulsed refuses to start in prod | The error names the variable | The prod profile refuses unsafe combinations (plain HTTP, no AOP CA, a non-loopback admin listener, test DNS provider); every profile refuses the removed `PULSE_AUTH_MODE`. Fix the configuration; do not switch profiles. |
| A tool gets `Authorization Violation` | The client's status and expiry on the Clients page | Rotate expired or revoked credentials. |
| A tool gets `Permissions Violation for … _INBOX.…` | The tool sets its own inbox prefix | Use the SDKs, or `--inbox-prefix _INBOX_<client-id>` with the nats CLI. |
| A tool gets `Permissions Violation for … CONSUMER.CREATE …` | Its grants | Subscribe and durable grants define which filters and consumer names are allowed; the SDKs create consumers the way the grants expect. |
| Clients reject the 4222 certificate | System page → Certificates | Until ACME succeeds, clients need the internal CA (`--ca`, `WithCAFile`, `ca=`). ACME errors are on the System page. |
| `curl` to the public API hangs or gets 403 | Security page (banned?), Cloudflare WAF events | Lift the ban; check the token's CIDR limits. |
| Alerts appear but no messages arrive | Alerts page → Notifiers (sent/failed/suppressed, last error) | Fix the token or webhook file; use "Test". Rate-limited messages count as suppressed. |
| Backups fail | Backups page error; `pulse backup ls` | S3 endpoint, credentials and bucket; the error names the failing step. |
| Disk filling up | Overview forecast; `disk_forecast` and `stream_usage` alerts | Add disk, or accept that `EVENTS` discards its oldest events at `max_bytes`. pulsed never shrinks `EVENTS`: a lower `PULSE_EVENTS_MAX_BYTES` is ignored with a warning. |
| Dashboard shows "not built" | The binary was built without `make web` | Use the release image; local builds need `make web` first. |
| Live tail shows "sampled out" | Tail rate limit (200/s per session) | The Explorer has every stored event. |
| Nobody can sign in to the dashboard | `pulsed` logs "no dashboard user exists yet" | Run `docker compose exec pulsed pulsed admin bootstrap` and open the link ([users.md](users.md)). |
| Locked out (lost TOTP, SSO down, SSO-only) | Audit log (`user.login`) | `docker compose exec pulsed pulsed admin recover --email you@example.com` ([users.md](users.md#break-glass)). |
| "too many attempts; try again later" | Sign-in throttling (per account and address) | Wait (at most 15 minutes) or an administrator clears it on the Users page. |
| SSO sends you back with `sso_error=…` | The provider's redirect URI and issuer | Register exactly `<PULSE_ADMIN_URL>/auth/oidc/callback`; run a test login on the provider page. |
| `pulse clients` says a token is required | | Create a personal access token (Account) and pass `--admin-token-file` or set `PULSE_ADMIN_TOKEN`. |
