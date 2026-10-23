-- Automatic and manual bans (PL-BAN-001..009). network is a canonical prefix: IPv4 /32, IPv6 /64.
CREATE TABLE bans (
  network    TEXT PRIMARY KEY,
  reason     TEXT NOT NULL,
  source     TEXT NOT NULL,
  manual     INTEGER NOT NULL DEFAULT 0,
  actor      TEXT NOT NULL DEFAULT 'system',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL, -- 0 = no expiry (manual bans only)
  offences   INTEGER NOT NULL DEFAULT 1
) STRICT;
CREATE INDEX bans_expires ON bans (expires_at);

-- Escalation memory: how often a network was banned recently.
CREATE TABLE ban_offences (
  network TEXT PRIMARY KEY,
  count   INTEGER NOT NULL,
  last_at INTEGER NOT NULL
) STRICT;

CREATE TABLE ban_allowlist (
  network    TEXT PRIMARY KEY,
  note       TEXT NOT NULL DEFAULT '',
  actor      TEXT NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;
