-- Rollups, checkpoints and the subject index (ADR-0020 as amended at M5). Buckets are unix seconds (UTC) at the start of
-- the minute/hour/day. dim: 'total' (key ''), 'p1'..'p4' (subject prefix of that depth), 'type' (ce-type or '(untyped)').
CREATE TABLE rollup_1m (
  stream TEXT NOT NULL, dim TEXT NOT NULL, key TEXT NOT NULL, bucket INTEGER NOT NULL,
  msgs INTEGER NOT NULL, bytes INTEGER NOT NULL,
  PRIMARY KEY (stream, dim, key, bucket)
) STRICT, WITHOUT ROWID;
CREATE INDEX rollup_1m_bucket ON rollup_1m (bucket);

CREATE TABLE rollup_1h (
  stream TEXT NOT NULL, dim TEXT NOT NULL, key TEXT NOT NULL, bucket INTEGER NOT NULL,
  msgs INTEGER NOT NULL, bytes INTEGER NOT NULL,
  PRIMARY KEY (stream, dim, key, bucket)
) STRICT, WITHOUT ROWID;
CREATE INDEX rollup_1h_bucket ON rollup_1h (bucket);

CREATE TABLE rollup_1d (
  stream TEXT NOT NULL, dim TEXT NOT NULL, key TEXT NOT NULL, bucket INTEGER NOT NULL,
  msgs INTEGER NOT NULL, bytes INTEGER NOT NULL,
  PRIMARY KEY (stream, dim, key, bucket)
) STRICT, WITHOUT ROWID;

-- Last stream sequence folded into the rollups; written in the same transaction as the counts (exactly once).
CREATE TABLE rollup_checkpoints (
  stream     TEXT PRIMARY KEY,
  last_seq   INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

-- Subject index (PL-ROLL-004), capped per stream; overflow is counted in subject_overflow.
CREATE TABLE subjects (
  stream          TEXT NOT NULL,
  subject         TEXT NOT NULL,
  first_seen      INTEGER NOT NULL,
  last_seen       INTEGER NOT NULL,
  last_seq        INTEGER NOT NULL,
  last_type       TEXT NOT NULL DEFAULT '',
  msgs            INTEGER NOT NULL,
  bytes           INTEGER NOT NULL,
  json_keys       TEXT NOT NULL DEFAULT '[]',
  keys_sampled_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (stream, subject)
) STRICT, WITHOUT ROWID;

CREATE TABLE subject_overflow (
  stream  TEXT PRIMARY KEY,
  dropped INTEGER NOT NULL
) STRICT;
