-- Backup catalog (PL-BAK-008, ADR-0035). The object store is the source of truth (refs.json per backup); this table
-- records what this server ran, including failures.
CREATE TABLE backups (
  id             TEXT PRIMARY KEY,
  trigger        TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
  status         TEXT NOT NULL CHECK (status IN ('running', 'ok', 'failed')),
  started_at     INTEGER NOT NULL,
  finished_at    INTEGER NOT NULL DEFAULT 0,
  error          TEXT NOT NULL DEFAULT '',
  logical_bytes  INTEGER NOT NULL DEFAULT 0, -- size of everything the backup represents
  uploaded_bytes INTEGER NOT NULL DEFAULT 0, -- what this run actually uploaded (new chunks + files)
  chunks_total   INTEGER NOT NULL DEFAULT 0,
  chunks_new     INTEGER NOT NULL DEFAULT 0,
  streams        TEXT NOT NULL DEFAULT '[]',  -- JSON [{name, msgs, bytes, first_seq, last_seq}]
  verified       INTEGER NOT NULL DEFAULT 0,
  verify_error   TEXT NOT NULL DEFAULT '',
  pruned_at      INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX backups_started ON backups (started_at);
