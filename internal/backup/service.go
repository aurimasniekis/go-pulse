package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/age"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/metrics"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// Keep is the GFS retention (PL-BAK-005).
type Keep struct {
	Daily, Weekly, Monthly int
}

// Options configure the service.
type Options struct {
	Store      ObjectStore
	Recipients []age.Recipient
	NameKey    []byte
	Keep       Keep
	At         string // HH:MM UTC, daily schedule (PL-BAK-001)
	KeysFile   string // keys.enc, backed up as a file (already sealed with the master key)
	TempDir    string // for the SQLite snapshot
	Version    string
	Log        *slog.Logger
	Now        func() time.Time
}

// Service runs backups and restores.
type Service struct {
	db *store.DB
	nc *nats.Conn
	js jetstream.JetStream
	o  Options

	mu      sync.Mutex   // one backup, prune or restore at a time
	running atomic.Value // string: what holds mu
	jobs    jobs
}

// New returns the service.
func New(db *store.DB, nc *nats.Conn, js jetstream.JetStream, o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Keep == (Keep{}) {
		o.Keep = Keep{Daily: 7, Weekly: 4, Monthly: 12}
	}
	if o.At == "" {
		o.At = "03:30"
	}
	return &Service{db: db, nc: nc, js: js, o: o}
}

// BackupRecord is a catalog row (PL-BAK-008).
type BackupRecord struct {
	ID            string        `json:"id"`
	Trigger       string        `json:"trigger" enum:"schedule,manual"`
	Status        string        `json:"status" enum:"running,ok,failed"`
	StartedAt     time.Time     `json:"started_at"`
	FinishedAt    time.Time     `json:"finished_at,omitzero"`
	DurationMs    int64         `json:"duration_ms"`
	Error         string        `json:"error,omitempty"`
	LogicalBytes  int64         `json:"logical_bytes" doc:"Size of everything the backup restores"`
	UploadedBytes int64         `json:"uploaded_bytes" doc:"What this run uploaded (new chunks and files)"`
	ChunksTotal   int           `json:"chunks_total"`
	ChunksNew     int           `json:"chunks_new"`
	Streams       []StreamStats `json:"streams"`
	Verified      bool          `json:"verified"`
	VerifyError   string        `json:"verify_error,omitempty"`
	Pruned        bool          `json:"pruned" doc:"Removed from the object store by retention"`
}

// StreamStats summarises one stream in a backup.
type StreamStats struct {
	Name     string `json:"name"`
	Msgs     uint64 `json:"msgs"`
	Bytes    uint64 `json:"bytes"`
	FirstSeq uint64 `json:"first_seq"`
	LastSeq  uint64 `json:"last_seq"`
}

// ErrBusy means another backup, prune or restore is running.
var ErrBusy = errors.New("a backup or restore is already running")

func (s *Service) lock(what string) error {
	if !s.mu.TryLock() {
		return ErrBusy
	}
	s.running.Store(what)
	return nil
}

func (s *Service) unlock() {
	s.running.Store("")
	s.mu.Unlock()
}

// Running names the operation in progress ("" when idle).
func (s *Service) Running() string {
	v, _ := s.running.Load().(string)
	return v
}

func newID(now time.Time) string {
	return now.UTC().Format("20060102T150405Z") + "-" + strings.ToLower(envelope.NewID()[20:])
}

// Run backs up daily at Options.At (UTC) and prunes after each success.
func (s *Service) Run(ctx context.Context) {
	for {
		next := nextRun(s.o.Now(), s.o.At)
		s.o.Log.Info("next backup", "at", next)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		if _, err := s.Backup(ctx, "schedule"); err != nil {
			s.o.Log.Error("backup failed", "err", err)
		}
	}
}

func nextRun(now time.Time, at string) time.Time {
	t, err := time.Parse("15:04", at)
	if err != nil {
		t = time.Date(0, 1, 1, 3, 30, 0, 0, time.UTC)
	}
	now = now.UTC()
	n := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	if !n.After(now) {
		n = n.Add(24 * time.Hour)
	}
	return n
}

// Backup takes one backup now, verifies it and applies retention.
func (s *Service) Backup(ctx context.Context, trigger string) (*BackupRecord, error) {
	if err := s.lock("backup"); err != nil {
		return nil, err
	}
	defer s.unlock()
	ctx = context.WithoutCancel(ctx) // a backup outlives the request that started it
	start := s.o.Now()
	rec := &BackupRecord{ID: newID(start), Trigger: trigger, Status: "running", StartedAt: start.UTC(), Streams: []StreamStats{}}
	if err := s.saveRecord(ctx, rec); err != nil {
		return nil, err
	}
	err := s.backup(ctx, rec)
	rec.FinishedAt = s.o.Now().UTC()
	rec.DurationMs = rec.FinishedAt.Sub(rec.StartedAt).Milliseconds()
	if err != nil {
		rec.Status, rec.Error = "failed", err.Error()
		metrics.Backups.WithLabelValues("failed").Inc()
	} else {
		rec.Status = "ok"
		metrics.Backups.WithLabelValues("ok").Inc()
		metrics.BackupUploadedBytes.Add(float64(rec.UploadedBytes))
	}
	if serr := s.saveRecord(ctx, rec); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return rec, err
	}
	s.o.Log.Info("backup done", "id", rec.ID, "logical_bytes", rec.LogicalBytes, "uploaded_bytes", rec.UploadedBytes,
		"chunks_new", rec.ChunksNew, "chunks_total", rec.ChunksTotal, "verified", rec.Verified, "ms", rec.DurationMs)
	if perr := s.prune(ctx); perr != nil {
		s.o.Log.Warn("backup retention", "err", perr)
	}
	return rec, nil
}

func (s *Service) encrypt(b []byte) ([]byte, error) { return seal(s.o.Recipients, b) }

func (s *Service) backup(ctx context.Context, rec *BackupRecord) error {
	if len(s.o.Recipients) == 0 || len(s.o.NameKey) == 0 || s.o.Store == nil {
		return errors.New("backups are not configured")
	}
	if c, ok := s.o.Store.(interface{ Check(context.Context) error }); ok {
		if err := c.Check(ctx); err != nil {
			return err
		}
	}
	known := map[string]bool{}
	existing, err := s.o.Store.List(ctx, chunkPrefix)
	if err != nil {
		return fmt.Errorf("list chunks: %w", err)
	}
	for _, o := range existing {
		known[strings.TrimPrefix(o.Key, chunkPrefix)] = true
	}
	ck := &chunker{ctx: ctx, store: s.o.Store, enc: s.encrypt, key: s.o.NameKey, known: known}
	m := Manifest{Version: 1, ID: rec.ID, Created: rec.StartedAt, PulsedVersion: s.o.Version}
	refs := Refs{ID: rec.ID, Created: rec.StartedAt}

	// Files: the SQLite database (online VACUUM INTO) and the sealed key set.
	files := map[string]func() ([]byte, error){
		"pulse.db": func() ([]byte, error) {
			dir, err := os.MkdirTemp(s.o.TempDir, "pulse-backup-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(dir)
			p := filepath.Join(dir, "pulse.db")
			if err := s.db.SnapshotTo(ctx, p); err != nil {
				return nil, fmt.Errorf("sqlite snapshot: %w", err)
			}
			return os.ReadFile(p) //nolint:gosec // our temp file
		},
	}
	if s.o.KeysFile != "" {
		files["keys.enc"] = func() ([]byte, error) { return os.ReadFile(s.o.KeysFile) }
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		b, err := files[name]()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		ct, err := s.encrypt(b)
		if err != nil {
			return err
		}
		key := fileKey(rec.ID, name)
		if err := s.o.Store.Put(ctx, key, ct); err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
		sum := sha256.Sum256(b)
		m.Files = append(m.Files, FileRef{Name: name, Object: key, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
		refs.Objects = append(refs.Objects, key)
		rec.UploadedBytes += int64(len(ct))
		rec.LogicalBytes += int64(len(b))
	}

	// Streams: every stream of the APP account, events and user streams, KV and object buckets.
	var streamNames []string
	l := s.js.StreamNames(ctx)
	for n := range l.Name() {
		streamNames = append(streamNames, n)
	}
	if err := l.Err(); err != nil {
		return fmt.Errorf("list streams: %w", err)
	}
	sort.Strings(streamNames)
	for _, name := range streamNames {
		sb, err := s.backupStream(ctx, name, ck)
		if err != nil {
			return err
		}
		m.Streams = append(m.Streams, *sb)
		rec.Streams = append(rec.Streams, StreamStats{Name: sb.Name, Msgs: sb.Msgs, Bytes: sb.Bytes, FirstSeq: sb.FirstSeq, LastSeq: sb.LastSeq})
		for _, b := range sb.Buckets {
			rec.LogicalBytes += b.Size
			refs.Chunks = append(refs.Chunks, b.Chunk)
		}
	}
	refs.Chunks = dedupe(refs.Chunks)
	rec.ChunksTotal, rec.ChunksNew = len(refs.Chunks), ck.newN
	rec.UploadedBytes += ck.newSize
	refs.LogicalBytes = rec.LogicalBytes

	mb, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ct, err := s.encrypt(mb)
	if err != nil {
		return err
	}
	if err := s.o.Store.Put(ctx, manifestKey(rec.ID), ct); err != nil {
		return fmt.Errorf("upload manifest: %w", err)
	}
	rec.UploadedBytes += int64(len(ct))
	refs.Objects = append(refs.Objects, manifestKey(rec.ID))
	refs.ManifestMAC = chunkName(s.o.NameKey, mb)
	rb, _ := json.Marshal(refs)
	if err := s.o.Store.Put(ctx, refsKey(rec.ID), rb); err != nil { // last: marks the backup complete
		return fmt.Errorf("upload refs: %w", err)
	}
	rec.Verified, rec.VerifyError = true, ""
	if err := s.verify(ctx, refs); err != nil {
		rec.Verified, rec.VerifyError = false, err.Error()
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (s *Service) backupStream(ctx context.Context, name string, ck *chunker) (*StreamBackup, error) {
	pr, pw := io.Pipe()
	type res struct {
		meta *StreamMeta
		err  error
	}
	done := make(chan res, 1)
	go func() {
		meta, err := Snapshot(ctx, s.nc, name, pw)
		_ = pw.CloseWithError(err)
		done <- res{meta, err}
	}()
	sb := &StreamBackup{Name: name}
	serr := splitSnapshot(pr, sb, ck)
	_ = pr.CloseWithError(serr)
	r := <-done
	if r.err != nil {
		return nil, r.err
	}
	if serr != nil {
		return nil, fmt.Errorf("stream %s: %w", name, serr)
	}
	sb.Meta = *r.meta
	var st struct {
		Msgs     uint64 `json:"messages"`
		Bytes    uint64 `json:"bytes"`
		FirstSeq uint64 `json:"first_seq"`
		LastSeq  uint64 `json:"last_seq"`
	}
	_ = json.Unmarshal(r.meta.State, &st)
	sb.Msgs, sb.Bytes, sb.FirstSeq, sb.LastSeq = st.Msgs, st.Bytes, st.FirstSeq, st.LastSeq
	return sb, nil
}

// verify checks that every object a backup references exists (PL-BAK-008).
func (s *Service) verify(ctx context.Context, refs Refs) error {
	have := map[string]bool{}
	objs, err := s.o.Store.List(ctx, chunkPrefix)
	if err != nil {
		return err
	}
	for _, o := range objs {
		have[strings.TrimPrefix(o.Key, chunkPrefix)] = true
	}
	missing := 0
	for _, c := range refs.Chunks {
		if !have[c] {
			missing++
		}
	}
	for _, k := range append(refs.Objects, refsKey(refs.ID)) {
		if _, err := s.o.Store.Stat(ctx, k); err != nil {
			missing++
		}
	}
	if missing > 0 {
		return fmt.Errorf("%d referenced objects are missing", missing)
	}
	return nil
}

func (s *Service) saveRecord(ctx context.Context, r *BackupRecord) error {
	streams, _ := json.Marshal(r.Streams)
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO backups(id, trigger, status, started_at, finished_at, error, logical_bytes, uploaded_bytes,
			chunks_total, chunks_new, streams, verified, verify_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET status = excluded.status, finished_at = excluded.finished_at, error = excluded.error,
			  logical_bytes = excluded.logical_bytes, uploaded_bytes = excluded.uploaded_bytes, chunks_total = excluded.chunks_total,
			  chunks_new = excluded.chunks_new, streams = excluded.streams, verified = excluded.verified, verify_error = excluded.verify_error`,
			r.ID, r.Trigger, r.Status, store.Ms(r.StartedAt), msOrZero(r.FinishedAt), r.Error, r.LogicalBytes, r.UploadedBytes,
			r.ChunksTotal, r.ChunksNew, string(streams), r.Verified, r.VerifyError)
		return err
	})
}

func msOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return store.Ms(t)
}

// List returns the catalog, newest first.
func (s *Service) List(ctx context.Context, limit int) ([]BackupRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.R().QueryContext(ctx, `SELECT id, trigger, status, started_at, finished_at, error, logical_bytes, uploaded_bytes,
		chunks_total, chunks_new, streams, verified, verify_error, pruned_at FROM backups ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupRecord{}
	for rows.Next() {
		var r BackupRecord
		var started, finished, pruned int64
		var streams string
		if err := rows.Scan(&r.ID, &r.Trigger, &r.Status, &started, &finished, &r.Error, &r.LogicalBytes, &r.UploadedBytes,
			&r.ChunksTotal, &r.ChunksNew, &streams, &r.Verified, &r.VerifyError, &pruned); err != nil {
			return nil, err
		}
		r.StartedAt = store.FromMs(started).UTC()
		if finished > 0 {
			r.FinishedAt = store.FromMs(finished).UTC()
			r.DurationMs = finished - started
		}
		r.Pruned = pruned > 0
		_ = json.Unmarshal([]byte(streams), &r.Streams)
		if r.Streams == nil {
			r.Streams = []StreamStats{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastStatus feeds the backup_stale alert: when the last backup succeeded, and the error if the latest finished run
// failed.
func (s *Service) LastStatus(ctx context.Context) (lastSuccess time.Time, lastError string, ok bool) {
	recs, err := s.List(ctx, 50)
	if err != nil {
		return time.Time{}, "", false
	}
	latest := true
	for _, r := range recs {
		if r.Status == "running" {
			continue
		}
		if latest && r.Status == "failed" {
			lastError = r.Error
		}
		latest = false
		if r.Status == "ok" {
			return r.FinishedAt, lastError, true
		}
	}
	return time.Time{}, lastError, true
}
