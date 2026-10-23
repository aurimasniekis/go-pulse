package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		n := e.Name()
		num, rest, ok := strings.Cut(n, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.HasSuffix(n, ".sql") {
			return nil, fmt.Errorf("store: bad migration file name %q (want NNNN_name.sql)", n)
		}
		b, err := migrationFS.ReadFile("migrations/" + n)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: strings.TrimSuffix(rest, ".sql"), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migrations must be numbered 1..n without gaps (found %d at position %d)", m.version, i+1)
		}
	}
	return out, nil
}

// SchemaVersion returns the newest migration this binary knows.
func SchemaVersion() int {
	ms, err := loadMigrations()
	if err != nil {
		return 0
	}
	return len(ms)
}

func migrate(ctx context.Context, w *sql.DB) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT`); err != nil {
		return err
	}
	var cur int
	if err := w.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&cur); err != nil {
		return err
	}
	if cur > len(ms) {
		return fmt.Errorf("store: database schema version %d is newer than this binary (%d); upgrade pulsed or restore a backup", cur, len(ms))
	}
	for _, m := range ms[cur:] {
		tx, err := w.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: migration %04d_%s: %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
			m.version, m.name, Ms(Now())); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
