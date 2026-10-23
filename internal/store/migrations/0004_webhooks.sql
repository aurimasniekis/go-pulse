-- Inbound and outbound webhooks and the delivery log (PL-HOOK-001..010). Secrets are sealed with the master key
-- (secretbox), because verifying and signing need the plaintext.
CREATE TABLE hooks_in (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL UNIQUE,
  kind             TEXT NOT NULL CHECK (kind IN ('github', 'stripe', 'hmac', 'secret')),
  subject          TEXT NOT NULL,
  secret_enc       BLOB NOT NULL,
  config           TEXT NOT NULL DEFAULT '{}',
  enabled          INTEGER NOT NULL DEFAULT 1,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  last_received_at INTEGER NOT NULL DEFAULT 0,
  received         INTEGER NOT NULL DEFAULT 0,
  rejected         INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE hooks_out (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  filter     TEXT NOT NULL,
  url        TEXT NOT NULL,
  secret_enc BLOB NOT NULL,
  headers    TEXT NOT NULL DEFAULT '{}',
  paused     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE deliveries (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  hook_id          TEXT NOT NULL,
  ts               INTEGER NOT NULL,
  seq              INTEGER NOT NULL,
  subject          TEXT NOT NULL,
  event_id         TEXT NOT NULL DEFAULT '',
  attempt          INTEGER NOT NULL,
  outcome          TEXT NOT NULL CHECK (outcome IN ('delivered', 'retry', 'dead', 'manual', 'test')),
  status_code      INTEGER NOT NULL DEFAULT 0,
  error            TEXT NOT NULL DEFAULT '',
  latency_ms       INTEGER NOT NULL DEFAULT 0,
  response_excerpt TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX deliveries_hook ON deliveries (hook_id, id);
CREATE INDEX deliveries_ts ON deliveries (ts);
