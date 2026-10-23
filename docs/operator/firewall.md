# Firewall and bans contract

> PL-BAN-007, ADR-0011. The deployment repo's `firewall` role owns the nftables ruleset; `pulsed banagent` only adds and
> removes elements in the two ban sets.

## Required table

`/etc/nftables.conf` must define (names are configurable, these are the defaults):

```nft
destroy table inet pulse_edge
table inet pulse_edge {
  set cloudflare_v4 { type ipv4_addr; flags interval; elements = { … } }   # from scripts/cf-ranges-seed.sh
  set cloudflare_v6 { type ipv6_addr; flags interval; elements = { … } }
  set pulse_ban_v4  { type ipv4_addr; flags interval, timeout; }
  set pulse_ban_v6  { type ipv6_addr; flags interval, timeout; }

  # priority raw runs before Docker's DNAT, so published ports are covered too.
  chain prerouting {
    type filter hook prerouting priority raw; policy accept;
    ip  saddr @pulse_ban_v4 drop
    ip6 saddr @pulse_ban_v6 drop
    tcp dport 443 ip  saddr != @cloudflare_v4 drop
    tcp dport 443 ip6 saddr != @cloudflare_v6 drop
  }
}
```

Never use `flush ruleset`: it would delete Docker's tables. `destroy table inet pulse_edge` (nft ≥ 1.0.8) replaces only
this table, so `systemctl restart nftables` keeps Docker working (and empties the ban sets until banagent refills them,
which it does on its next poll).

## banagent

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_BANAGENT_URL` | `http://127.0.0.1:9464/internal/v1/bans` | Ban feed on pulsed's metrics listener (host loopback). |
| `PULSE_BANAGENT_TOKEN_FILE` | `/run/pulse-banagent/token` | Bearer token; mount only the `banagent` subpath of the pulsed volume, read-only. |
| `PULSE_BANAGENT_TABLE` | `pulse_edge` | inet table name. |
| `PULSE_BANAGENT_SET_V4` / `_V6` | `pulse_ban_v4` / `pulse_ban_v6` | Set names. |

Run with `network_mode: host` and `cap_add: [NET_ADMIN]`. banagent long-polls the feed (new bans arrive within a
second), re-reads everything every 5 minutes, and leaves the sets untouched while pulsed is down; element timeouts still
expire bans. A single IPv6 address is banned as its /64.

## Never banned

Loopback, RFC 1918, link-local, `0.0.0.0/8`, IPv6 loopback, ULA and link-local, Cloudflare's ranges, `PULSE_BAN_ALLOW`,
`PULSE_HOST_IPS` and the dashboard allowlist. Host SSH is protected by fail2ban, not by pulsed.
