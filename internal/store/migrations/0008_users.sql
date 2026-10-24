-- Dashboard users, sessions and single sign-on (ADR-0038, PL-USR-*). Ported from hallpass's operator auth.
-- Timestamps are unix milliseconds. Secrets are never stored in clear: passwords are Argon2id PHC strings, tokens and
-- recovery codes are keyed MACs, TOTP and OIDC client secrets are sealed with a key derived from the master key.

CREATE TABLE users (
  id              TEXT PRIMARY KEY,
  email           TEXT UNIQUE COLLATE NOCASE,
  display_name    TEXT NOT NULL,
  enabled         INTEGER NOT NULL CHECK (enabled IN (0, 1)),
  provisioned_via TEXT NOT NULL CHECK (provisioned_via IN ('bootstrap', 'admin', 'invite', 'jit', 'recovery')),
  mfa_required    INTEGER NOT NULL DEFAULT 0 CHECK (mfa_required IN (0, 1)),
  version         INTEGER NOT NULL DEFAULT 1,
  created_at      INTEGER NOT NULL,
  created_by      TEXT NOT NULL,
  updated_at      INTEGER NOT NULL,
  last_login_at   INTEGER
) STRICT;

CREATE TABLE user_passwords (
  user_id        TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  password_phc   TEXT NOT NULL,
  params_version INTEGER NOT NULL,
  changed_at     INTEGER NOT NULL
) STRICT;

-- source: manual (set by an administrator) or sso (replaced at every SSO login from the provider's mappings).
CREATE TABLE user_roles (
  user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  role       TEXT NOT NULL CHECK (role IN ('administrator', 'operator', 'auditor', 'viewer')),
  source     TEXT NOT NULL CHECK (source IN ('manual', 'sso')),
  source_ref TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, role, source, source_ref)
) STRICT;

CREATE TABLE user_totp (
  user_id      TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  secret_enc   BLOB NOT NULL,
  confirmed_at INTEGER NOT NULL,
  last_step    INTEGER NOT NULL DEFAULT 0
) STRICT;

-- An enrollment in progress; it replaces user_totp only when confirmed, so an abandoned re-enrollment never removes
-- the working factor.
CREATE TABLE user_totp_pending (
  user_id    TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  secret_enc BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT;

CREATE TABLE user_recovery_codes (
  user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  code_mac   BLOB NOT NULL,
  used_at    INTEGER,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, code_mac)
) STRICT;

CREATE TABLE one_time_tokens (
  mac        BLOB PRIMARY KEY,
  kind       TEXT NOT NULL CHECK (kind IN ('setup', 'invite', 'reset', 'recovery_login', 'link')),
  user_id    TEXT,
  data       TEXT NOT NULL DEFAULT '{}',
  expires_at INTEGER NOT NULL,
  used_at    INTEGER,
  created_at INTEGER NOT NULL,
  created_by TEXT NOT NULL
) STRICT;
CREATE INDEX one_time_tokens_kind ON one_time_tokens (kind, expires_at);

CREATE TABLE web_sessions (
  id_hash             BLOB PRIMARY KEY,
  display_id          TEXT NOT NULL UNIQUE,
  user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  csrf_secret         BLOB NOT NULL,
  auth_method         TEXT NOT NULL CHECK (auth_method IN ('password', 'oidc', 'breakglass')),
  auth_provider       TEXT NOT NULL DEFAULT '',
  listener            TEXT NOT NULL DEFAULT '',
  auth_at             INTEGER NOT NULL,
  mfa_at              INTEGER,
  created_at          INTEGER NOT NULL,
  last_seen_at        INTEGER NOT NULL,
  absolute_expires_at INTEGER NOT NULL,
  ip                  TEXT NOT NULL DEFAULT '',
  user_agent          TEXT NOT NULL DEFAULT '',
  revoked_at          INTEGER,
  revoke_reason       TEXT
) STRICT;
CREATE INDEX web_sessions_user ON web_sessions (user_id, revoked_at);

-- Personal access tokens for the pulse CLI. Accepted only on the loopback admin listener.
CREATE TABLE user_tokens (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  prefix       TEXT NOT NULL UNIQUE,
  mac          BLOB NOT NULL UNIQUE,
  role         TEXT NOT NULL CHECK (role IN ('administrator', 'operator', 'auditor', 'viewer')),
  expires_at   INTEGER NOT NULL,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  last_used_ip TEXT NOT NULL DEFAULT '',
  revoked_at   INTEGER
) STRICT;
CREATE INDEX user_tokens_user ON user_tokens (user_id);

CREATE TABLE login_throttle (
  key             TEXT PRIMARY KEY,
  failures        INTEGER NOT NULL,
  next_allowed_at INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
) STRICT;

CREATE TABLE auth_settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  updated_by TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE oidc_providers (
  id                           TEXT PRIMARY KEY,
  slug                         TEXT NOT NULL UNIQUE,
  display_name                 TEXT NOT NULL,
  issuer                       TEXT NOT NULL,
  client_id                    TEXT NOT NULL,
  client_secret_enc            BLOB,
  scopes                       TEXT NOT NULL,
  groups_claim                 TEXT NOT NULL DEFAULT '',
  provisioning                 TEXT NOT NULL CHECK (provisioning IN ('preprovisioned', 'jit')),
  jit                          TEXT NOT NULL DEFAULT '{}',
  allow_private_network        INTEGER NOT NULL DEFAULT 0 CHECK (allow_private_network IN (0, 1)),
  allow_cross_origin_endpoints INTEGER NOT NULL DEFAULT 0 CHECK (allow_cross_origin_endpoints IN (0, 1)),
  ca_bundle_pem                TEXT NOT NULL DEFAULT '',
  stepup_fallback              TEXT NOT NULL DEFAULT 'none' CHECK (stepup_fallback IN ('none', 'local_totp')),
  acr_values                   TEXT NOT NULL DEFAULT '',
  end_session_enabled          INTEGER NOT NULL DEFAULT 0 CHECK (end_session_enabled IN (0, 1)),
  enabled                      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
  test_login_ok_at             INTEGER,
  test_login_by                TEXT NOT NULL DEFAULT '',
  version                      INTEGER NOT NULL DEFAULT 1,
  created_at                   INTEGER NOT NULL,
  updated_at                   INTEGER NOT NULL
) STRICT;

CREATE TABLE oidc_mappings (
  id          TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
  group_value TEXT NOT NULL,
  role        TEXT NOT NULL CHECK (role IN ('administrator', 'operator', 'auditor', 'viewer')),
  created_at  INTEGER NOT NULL,
  UNIQUE (provider_id, group_value, role)
) STRICT;

-- An identity is (issuer, subject); email is never used to match accounts.
CREATE TABLE user_identities (
  issuer        TEXT NOT NULL,
  subject       TEXT NOT NULL,
  provider_id   TEXT NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
  user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  linked_via    TEXT NOT NULL CHECK (linked_via IN ('admin', 'self_link', 'jit')),
  created_at    INTEGER NOT NULL,
  last_login_at INTEGER,
  PRIMARY KEY (issuer, subject)
) STRICT;
CREATE INDEX user_identities_user ON user_identities (user_id);

CREATE TABLE oidc_auth_states (
  state_hash      BLOB PRIMARY KEY,
  provider_id     TEXT NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
  purpose         TEXT NOT NULL CHECK (purpose IN ('login', 'stepup', 'test', 'link')),
  nonce_hash      BLOB NOT NULL,
  verifier_enc    BLOB NOT NULL,
  binding_hash    BLOB NOT NULL,
  return_to       TEXT NOT NULL DEFAULT '/',
  user_id         TEXT,
  session_display TEXT,
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL
) STRICT;
CREATE INDEX oidc_auth_states_expiry ON oidc_auth_states (expires_at);
