package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

func TestPragmasOnEveryConnection(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	check := func(pool *sql.DB, n int, wantQueryOnly int) {
		var wg sync.WaitGroup
		conns := make([]*sql.Conn, n)
		for i := range n {
			c, err := pool.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			conns[i] = c
		}
		for _, c := range conns {
			wg.Add(1)
			go func(c *sql.Conn) {
				defer wg.Done()
				var mode string
				var sync, fk, qo int
				if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
					t.Errorf("journal_mode=%q %v", mode, err)
				}
				if err := c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil || sync != 2 {
					t.Errorf("synchronous=%d %v", sync, err)
				}
				if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
					t.Errorf("foreign_keys=%d %v", fk, err)
				}
				if err := c.QueryRowContext(ctx, "PRAGMA query_only").Scan(&qo); err != nil || qo != wantQueryOnly {
					t.Errorf("query_only=%d %v", qo, err)
				}
			}(c)
		}
		wg.Wait()
		for _, c := range conns {
			_ = c.Close()
		}
	}
	check(db.writer, 1, 0)
	check(db.reader, 4, 1)
	if _, err := db.R().ExecContext(ctx, "INSERT INTO meta(key, value) VALUES ('x', 'y')"); err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestMigrationsAndReopen(t *testing.T) {
	db, dir := openTemp(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO meta(key, value) VALUES ('k', 'v')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db2, err := Open(ctx, Options{Path: filepath.Join(dir, "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var v string
	var ver int
	if err := db2.R().QueryRowContext(ctx, "SELECT value FROM meta WHERE key='k'").Scan(&v); err != nil || v != "v" {
		t.Fatalf("%q %v", v, err)
	}
	if err := db2.R().QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&ver); err != nil || ver != SchemaVersion() {
		t.Fatalf("version %d want %d (%v)", ver, SchemaVersion(), err)
	}
}

func TestExclusiveLock(t *testing.T) {
	_, dir := openTemp(t)
	_, err := Open(context.Background(), Options{Path: filepath.Join(dir, "pulse.db")})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second open: %v", err)
	}
}

func TestRefuseNewerSchema(t *testing.T) {
	db, dir := openTemp(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, applied_at) VALUES (9999, 'future', 0)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(ctx, Options{Path: filepath.Join(dir, "pulse.db")}); err == nil {
		t.Fatal("opened a database with a newer schema")
	}
}

func TestSnapshotAndFailedWriteRollsBack(t *testing.T) {
	db, dir := openTemp(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key, value) VALUES ('a', 'b')"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	_ = db.R().QueryRowContext(ctx, "SELECT COUNT(*) FROM meta").Scan(&n)
	if n != 0 {
		t.Fatal("rolled back write is visible")
	}
	snap := filepath.Join(dir, "snap.db")
	if err := db.SnapshotTo(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := db.SnapshotTo(ctx, snap); err == nil {
		t.Fatal("snapshot overwrote an existing file")
	}
}

func TestCorruptDatabaseRefused(t *testing.T) {
	db, dir := openTemp(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		for i := range 2000 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO audit(ts, actor, action, result) VALUES (?, 'a', 'x', 'ok')", i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	p := filepath.Join(dir, "pulse.db")
	// Fold the WAL into the main file, then scribble over a page in the middle.
	raw, _ := sql.Open("sqlite", "file:"+p)
	_, _ = raw.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_ = raw.Close()
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	junk := make([]byte, 4096)
	for i := range junk {
		junk[i] = 0xA5
	}
	_, _ = f.WriteAt(junk, st.Size()/2/4096*4096)
	_ = f.Close()
	if _, err := Open(ctx, Options{Path: p}); err == nil {
		t.Fatal("opened a corrupted database")
	}
}

func TestDatabaseFileMode(t *testing.T) {
	db, _ := openTemp(t)
	st, err := os.Stat(db.Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode().Perm(), err)
	}
}
