-- pulsed state database, first schema (ADR-0024). Timestamps are unix milliseconds.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

-- Tool clients (PL-AUTH-002). Grants are a JSON array of natsauth.Grant.
CREATE TABLE clients (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  tags        TEXT NOT NULL DEFAULT '[]',
  grants      TEXT NOT NULL DEFAULT '[]',
  user_key    TEXT NOT NULL UNIQUE,
  signing_key TEXT NOT NULL UNIQUE,
  status      TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
  expires_at  INTEGER NOT NULL,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  revoked_at  INTEGER,
  version     INTEGER NOT NULL DEFAULT 1
) STRICT;

-- Revoked user keys (revocation and rotation), kept until the JWT would have expired anyway.
CREATE TABLE client_revocations (
  user_key       TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL,
  revoked_at     INTEGER NOT NULL,
  jwt_expires_at INTEGER NOT NULL
) STRICT;

-- Audit log (PL-AUD-001): append-only for the application.
CREATE TABLE audit (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  ts      INTEGER NOT NULL,
  actor   TEXT NOT NULL,
  action  TEXT NOT NULL,
  target  TEXT NOT NULL DEFAULT '',
  ip      TEXT NOT NULL DEFAULT '',
  result  TEXT NOT NULL CHECK (result IN ('ok', 'denied', 'error')),
  details TEXT NOT NULL DEFAULT '{}'
) STRICT;
CREATE INDEX audit_ts ON audit (ts);
CREATE INDEX audit_action_ts ON audit (action, ts);
