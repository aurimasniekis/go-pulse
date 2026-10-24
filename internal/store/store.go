// Package store is pulsed's SQLite state database (ADR-0024, PL-DB-001..007).
//
// One writer connection (BEGIN IMMEDIATE) and a small pool of query_only readers share one WAL database file.
// An exclusive flock on the lock file guarantees a single owner per data directory. Write transactions must never
// span network I/O (PL-DB-004): callers prepare everything first and pass a short closure to Write.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DB is the opened state database.
type DB struct {
	path   string
	writer *sql.DB
	reader *sql.DB
	lock   *os.File
}

var pragmas = []string{
	"journal_mode(WAL)",
	"synchronous(FULL)",
	"foreign_keys(ON)",
	"busy_timeout(5000)",
	"trusted_schema(OFF)",
	"temp_store(MEMORY)",
	"cache_size(-16000)",
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(ON)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Options for Open.
type Options struct {
	Path     string // database file
	LockPath string // flock file; defaults to <dir>/pulsed.lock
	Readers  int    // reader pool size (default 4)
	// Shared lets a short-lived command (pulsed admin) open the database while pulsed serve holds the lock. It then
	// runs no migrations and requires the schema to be exactly this binary's version.
	Shared bool
}

// ErrLocked means another process owns the data directory.
var ErrLocked = errors.New("store: another pulsed process owns this data directory")

// Open locks the data directory, opens the database, checks integrity and applies migrations.
func Open(ctx context.Context, o Options) (*DB, error) {
	dir := filepath.Dir(o.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := checkLocalFS(dir); err != nil {
		return nil, err
	}
	if o.LockPath == "" {
		o.LockPath = filepath.Join(dir, "pulsed.lock")
	}
	if o.Readers <= 0 {
		o.Readers = 4
	}
	lock, err := acquireLock(o.LockPath)
	shared := false
	if errors.Is(err, ErrLocked) && o.Shared {
		lock, err, shared = nil, nil, true
	}
	if err != nil {
		return nil, err
	}
	db := &DB{path: o.Path, lock: lock}
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	if f, err := os.OpenFile(o.Path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		_ = f.Close()
	} else {
		return nil, err
	}
	if err := os.Chmod(o.Path, 0o600); err != nil {
		return nil, err
	}
	db.writer, err = sql.Open("sqlite", dsn(o.Path, false))
	if err != nil {
		return nil, err
	}
	db.writer.SetMaxOpenConns(1)
	db.writer.SetMaxIdleConns(1)
	db.writer.SetConnMaxLifetime(0)
	if err := db.writer.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("store: open %s: %w", o.Path, err)
	}
	if err := quickCheck(ctx, db.writer); err != nil {
		return nil, err
	}
	if shared {
		if err := requireCurrentSchema(ctx, db.writer); err != nil {
			return nil, err
		}
	} else if err := migrate(ctx, db.writer); err != nil {
		return nil, err
	}
	db.reader, err = sql.Open("sqlite", dsn(o.Path, true))
	if err != nil {
		return nil, err
	}
	db.reader.SetMaxOpenConns(o.Readers)
	db.reader.SetMaxIdleConns(o.Readers)
	if err := db.reader.PingContext(ctx); err != nil {
		return nil, err
	}
	ok = true
	return db, nil
}

func quickCheck(ctx context.Context, w *sql.DB) error {
	var res string
	if err := w.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return fmt.Errorf("store: quick_check: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("store: database integrity check failed (%s); restore pulse.db from a backup (docs/operator/restore.md)", res)
	}
	return nil
}

// Close closes the pools and releases the lock.
func (db *DB) Close() error {
	var errs []error
	if db.reader != nil {
		errs = append(errs, db.reader.Close())
	}
	if db.writer != nil {
		errs = append(errs, db.writer.Close())
	}
	if db.lock != nil {
		errs = append(errs, releaseLock(db.lock))
	}
	return errors.Join(errs...)
}

// Path returns the database file path.
func (db *DB) Path() string { return db.path }

// R returns the read-only pool.
func (db *DB) R() *sql.DB { return db.reader }

// Write runs fn in a write transaction (BEGIN IMMEDIATE). fn must not do network I/O (PL-DB-004).
func (db *DB) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// SnapshotTo writes a consistent copy of the database to path with VACUUM INTO (used by backups, PL-BAK-003).
func (db *DB) SnapshotTo(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("store: snapshot target %s exists", path)
	}
	// A dedicated connection: query_only readers reject VACUUM INTO, and the writer must stay free for writes.
	snap, err := sql.Open("sqlite", dsn(db.path, false))
	if err != nil {
		return err
	}
	defer snap.Close()
	snap.SetMaxOpenConns(1)
	_, err = snap.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

// Now returns the current time truncated to milliseconds; all timestamps are stored as unix milliseconds.
func Now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// Ms converts a time to unix milliseconds (0 for the zero time).
func Ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// FromMs converts unix milliseconds to a time (zero for 0).
func FromMs(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
