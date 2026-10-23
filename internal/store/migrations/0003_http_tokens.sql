-- HTTP bearer tokens for the public API (PL-ING-001). Only a SHA-256 of the secret is stored.
CREATE TABLE http_tokens (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  description  TEXT NOT NULL DEFAULT '',
  prefix       TEXT NOT NULL UNIQUE,
  secret_hash  BLOB NOT NULL,
  grants       TEXT NOT NULL DEFAULT '[]',
  cidrs        TEXT NOT NULL DEFAULT '[]',
  expires_at   INTEGER NOT NULL,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  last_used_ip TEXT NOT NULL DEFAULT '',
  revoked_at   INTEGER
) STRICT;
