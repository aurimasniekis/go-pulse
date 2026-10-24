-- Alert rules, live state and history (PL-OBS-004/005, ADR-0033).
CREATE TABLE alert_rules (
  id          TEXT PRIMARY KEY,           -- built-in: the kind; custom: custom_<random>
  kind        TEXT NOT NULL,
  builtin     INTEGER NOT NULL,
  name        TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1,
  severity    TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
  params      TEXT NOT NULL DEFAULT '{}', -- JSON, per kind
  for_seconds INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
) STRICT;

-- One row per (rule, key) whose condition currently holds.
CREATE TABLE alert_state (
  rule_id       TEXT NOT NULL,
  key           TEXT NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('pending', 'firing')),
  since         INTEGER NOT NULL,
  fired_at      INTEGER NOT NULL DEFAULT 0,
  last_notified INTEGER NOT NULL DEFAULT 0,
  value         REAL NOT NULL DEFAULT 0,
  summary       TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (rule_id, key)
) STRICT;

CREATE TABLE alert_history (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  ts        INTEGER NOT NULL,
  rule_id   TEXT NOT NULL,
  rule_name TEXT NOT NULL,
  key       TEXT NOT NULL,
  state     TEXT NOT NULL CHECK (state IN ('firing', 'resolved')),
  severity  TEXT NOT NULL,
  summary   TEXT NOT NULL,
  value     REAL NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX alert_history_ts ON alert_history (ts);
CREATE INDEX alert_history_rule ON alert_history (rule_id, id);
