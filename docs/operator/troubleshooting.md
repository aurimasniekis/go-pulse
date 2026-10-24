# Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| `pulsed` stays unhealthy | `docker compose logs pulsed`; `curl 127.0.0.1:9464/readyz` | Usually NATS is not reachable or the APP account push failed: check `nats` health and `PULSE_NATS_URL`. |
| pulsed refuses to start in prod | The error names the variable | The prod profile refuses unsafe combinations (plain HTTP, no AOP CA, admin exposed with auth none, test DNS provider). Fix the configuration; do not switch profiles. |
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
