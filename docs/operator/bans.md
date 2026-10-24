# Bans

pulsed bans addresses that keep failing authentication, fail2ban-style (ADR-0011, ADR-0031).

## Detection

Failures count per address (IPv4 /32, IPv6 /64) within `PULSE_BAN_WINDOW` (10 min). Three sources count:
- NATS authentication errors (`CLIENT.AUTH.ERR` advisories);
- missing or invalid HTTP tokens;
- bad webhook signatures.

`PULSE_BAN_THRESHOLD` failures (5) ban the address for `PULSE_BAN_DURATION` (1 h). Repeat offenders get ×4 each
time, up to `PULSE_BAN_MAX` (30 days). Offence memory lasts 30 days after a ban ends.

Never banned:
- loopback, private and link-local ranges;
- Cloudflare's ranges;
- `PULSE_HOST_IPS`, `PULSE_BAN_ALLOW` and the dashboard allowlist.

## Enforcement

1. **HTTP**: banned addresses get 403 on every public route.
2. **nftables**: banagent mirrors bans into the `pulse_ban_v4/v6` sets with timeouts, so packets drop before NATS
   or Docker see them ([firewall.md](firewall.md)).
3. **Cloudflare IP list** (optional, `PULSE_CF_*`): bans of 1 h or more are added to an account list, which the
   Terraform WAF rule blocks at the edge.

## Managing

- Security page: active bans, history, recent failures, offenders, the allowlist, manual bans and lifting.
- API (through the tunnel): `POST /api/admin/v1/security/bans/lift` with `{"network":"203.0.113.7"}`; header
  `X-Pulse-CSRF: 1`.
- The `ban_storm` alert fires at 10 bans in 10 minutes.
- Locked yourself out of NATS? The admin tunnel arrives from loopback and is never banned: lift the ban on the
  Security page.
