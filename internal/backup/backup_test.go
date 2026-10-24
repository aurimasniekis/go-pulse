package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/nats-io/nats-server/v2/server/archive"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

type rig struct {
	*drillEnv
	db    *store.DB
	mem   *Mem
	svc   *Service
	id    *age.X25519Identity
	key   []byte
	keys  string
	clock time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	d := newDrillEnv(t)
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "state", "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id, _ := age.GenerateX25519Identity()
	key, _ := NameKey(bytes.Repeat([]byte{7}, 32))
	keys := filepath.Join(t.TempDir(), "keys.enc")
	_ = os.WriteFile(keys, []byte("sealed-key-set"), 0o600)
	r := &rig{drillEnv: d, db: db, mem: NewMem(), id: id, key: key, keys: keys, clock: time.Now()}
	r.svc = New(db, d.nc, d.js, Options{Store: r.mem, Recipients: []age.Recipient{id.Recipient()}, NameKey: key, KeysFile: keys,
		TempDir: t.TempDir(), Version: "test", Now: func() time.Time { return r.clock }})
	return r
}

func (r *rig) reader() *Reader {
	return &Reader{Store: r.mem, Identities: []age.Identity{r.id}, NameKey: r.key}
}

// publishBig writes n events of size bytes each.
func (r *rig) publishBig(t *testing.T, subject string, n, size int) {
	t.Helper()
	payload := bytes.Repeat([]byte("x"), size)
	for i := range n {
		m, _ := envelope.Encode(envelope.Event{Subject: subject, Type: "big.v1", Data: append(fmt.Appendf(nil, "%06d", i), payload...)})
		if _, err := r.js.PublishMsg(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.publishBig(t, "evt.big.blob.put", 400, 10<<10) // ~4 MB: chunked parts, multi-chunk snapshot transfer with acks
	kv, err := r.js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "settings"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = kv.PutString(ctx, "mode", "night")
	obj, err := r.js.CreateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: "files"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := obj.PutBytes(ctx, "doc.txt", bytes.Repeat([]byte("doc "), 50000)); err != nil {
		t.Fatal(err)
	}
	before := r.read(t, streams.Events)

	rec, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "ok" || !rec.Verified || rec.ChunksNew == 0 || rec.ChunksNew != rec.ChunksTotal || rec.LogicalBytes < 4<<20 || len(rec.Streams) != 3 {
		t.Fatalf("%+v", rec)
	}
	// Nothing in the store is plaintext.
	for k, v := range r.mem.objs {
		if strings.HasSuffix(k, "refs.json") {
			if bytes.Contains(v, []byte("EVENTS")) || bytes.Contains(v, []byte("settings")) {
				t.Fatalf("refs leaks names: %s", v)
			}
			continue
		}
		if !bytes.HasPrefix(v, []byte("age-encryption.org/v1")) || bytes.Contains(v, []byte("xxxxxxxx")) || bytes.Contains(v, []byte("night")) {
			t.Fatalf("%s is not age ciphertext", k)
		}
	}
	list, _ := r.svc.List(ctx, 10)
	if len(list) != 1 || list[0].ID != rec.ID || list[0].Status != "ok" {
		t.Fatalf("catalog %+v", list)
	}

	// Disaster: all streams gone.
	for _, s := range []string{streams.Events, "KV_settings", "OBJ_files"} {
		if err := r.js.DeleteStream(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	rd := r.reader()
	m, err := rd.Manifest(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := rd.RestoreStreams(ctx, r.nc, r.js, m, RestoreOptions{DryRun: true})
	if err != nil || len(plan) != 3 || plan[0].Action != "restore" || plan[0].Result != "" {
		t.Fatalf("dry run %+v %v", plan, err)
	}
	if _, err := r.js.Stream(ctx, streams.Events); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Fatal("dry run restored something")
	}
	// A selection restores only the named streams (regression: the filter used to open up after the first match).
	sel, err := rd.RestoreStreams(ctx, r.nc, r.js, m, RestoreOptions{Streams: []string{"KV_settings"}, DryRun: true})
	if err != nil || len(sel) != 1 || sel[0].Name != "KV_settings" {
		t.Fatalf("selection %+v %v", sel, err)
	}
	if _, err := rd.RestoreStreams(ctx, r.nc, r.js, m, RestoreOptions{Streams: []string{"NOPE"}, DryRun: true}); err == nil {
		t.Fatal("unknown stream accepted")
	}
	res, err := rd.RestoreStreams(ctx, r.nc, r.js, m, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res {
		if p.Result != "restored" {
			t.Fatalf("%+v", res)
		}
	}
	after := r.read(t, streams.Events)
	if len(after) != len(before) {
		t.Fatalf("restored %d of %d", len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("event %d differs", i)
		}
	}
	kv, _ = r.js.KeyValue(ctx, "settings")
	if e, err := kv.Get(ctx, "mode"); err != nil || string(e.Value()) != "night" {
		t.Fatalf("kv after restore: %v", err)
	}
	obj, _ = r.js.ObjectStore(ctx, "files")
	if b, err := obj.GetBytes(ctx, "doc.txt"); err != nil || len(b) != 200000 {
		t.Fatalf("object after restore: %d %v", len(b), err)
	}
	// Files.
	dir := t.TempDir()
	if err := rd.WriteFile(ctx, m, "keys.enc", filepath.Join(dir, "keys.enc"), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "keys.enc")); string(b) != "sealed-key-set" {
		t.Fatalf("keys.enc %q", b)
	}
	if err := rd.WriteFile(ctx, m, "keys.enc", filepath.Join(dir, "keys.enc"), false); err == nil {
		t.Fatal("overwrote without permission")
	}
	if err := rd.WriteFile(ctx, m, "pulse.db", filepath.Join(dir, "pulse.db"), false); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = restored.R().QueryRowContext(ctx, "SELECT COUNT(*) FROM backups").Scan(&n)
	_ = restored.Close()
	if n != 1 { // the snapshot is taken while this backup's row says "running"
		t.Fatalf("restored database has %d backup rows", n)
	}
}

func TestIncrementalDedupe(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.publishBig(t, "evt.big.blob.put", 1200, 10<<10) // ~12 MB: several message blocks
	first, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	r.clock = r.clock.Add(time.Hour)
	second, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if second.ChunksNew > 1 || second.UploadedBytes >= first.UploadedBytes/4 {
		t.Fatalf("unchanged data re-uploaded: first %+v second %+v", first, second)
	}
	r.publishBig(t, "evt.big.blob.put", 50, 10<<10)
	r.clock = r.clock.Add(time.Hour)
	third, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if third.ChunksNew == 0 || third.ChunksNew >= first.ChunksNew || third.UploadedBytes >= first.UploadedBytes/2 {
		t.Fatalf("append uploaded too much: first %+v third %+v", first, third)
	}
	t.Logf("first: %d chunks %d B; unchanged: %d new %d B; +50 events: %d new %d B",
		first.ChunksNew, first.UploadedBytes, second.ChunksNew, second.UploadedBytes, third.ChunksNew, third.UploadedBytes)
	// Each backup is complete on its own (earlier same-day backups were pruned by GFS: newest per day wins).
	m, err := r.reader().Manifest(ctx, third.ID)
	if err != nil || len(m.Streams) == 0 {
		t.Fatal(err)
	}
	if _, err := r.reader().Manifest(ctx, second.ID); err == nil {
		t.Fatal("same-day backup kept")
	}
}

func TestTamperAndWrongIdentity(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.publishBig(t, "evt.big.blob.put", 300, 10<<10)
	rec, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := age.GenerateX25519Identity()
	if _, err := (&Reader{Store: r.mem, Identities: []age.Identity{other}}).Manifest(ctx, rec.ID); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong identity: %v", err)
	}
	// A manifest substituted by someone who only knows the (public) recipient is rejected.
	fake, _ := json.Marshal(Manifest{Version: 1, ID: rec.ID})
	ct, _ := seal([]age.Recipient{r.id.Recipient()}, fake)
	orig, _ := r.mem.Get(ctx, manifestKey(rec.ID))
	_ = r.mem.Put(ctx, manifestKey(rec.ID), ct)
	if _, err := r.reader().Manifest(ctx, rec.ID); !errors.Is(err, ErrTampered) {
		t.Fatalf("substituted manifest: %v", err)
	}
	_ = r.mem.Put(ctx, manifestKey(rec.ID), orig)

	// A damaged chunk stops a replace before the existing stream is deleted.
	m, err := r.reader().Manifest(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	var victim string
	for _, sb := range m.Streams {
		if len(sb.Buckets) > 0 {
			victim = sb.Buckets[len(sb.Buckets)/2].Chunk
		}
	}
	r.mem.Corrupt(chunkPrefix + victim)
	_, err = r.reader().RestoreStreams(ctx, r.nc, r.js, m, RestoreOptions{Streams: []string{streams.Events}, Replace: true})
	if err == nil {
		t.Fatal("restore with a damaged chunk succeeded")
	}
	if got := r.read(t, streams.Events); len(got) != 300 {
		t.Fatalf("existing stream damaged by a failed restore: %d events", len(got))
	}
	// The catalog's verification does not read contents, but missing objects are reported.
	_ = r.mem.Delete(ctx, chunkPrefix+victim)
	if err := r.svc.verify(ctx, Refs{ID: rec.ID, Chunks: []string{victim}}); err == nil {
		t.Fatal("verify missed a missing chunk")
	}
}

func TestKeepSet(t *testing.T) {
	var bs []Refs
	start := time.Date(2026, 10, 24, 3, 30, 0, 0, time.UTC)
	for i := range 400 { // one per day, newest first
		bs = append(bs, Refs{ID: fmt.Sprint(i), Created: start.AddDate(0, 0, -i)})
	}
	bs = append([]Refs{{ID: "extra", Created: start.Add(time.Hour)}}, bs...) // a second backup today (newest)
	keep := keepSet(bs, Keep{Daily: 7, Weekly: 4, Monthly: 12})
	if !keep["extra"] || keep["0"] {
		t.Fatal("the newest backup of a day wins; the older one of the same day goes")
	}
	for i := 1; i <= 6; i++ {
		if !keep[fmt.Sprint(i)] {
			t.Fatalf("daily %d not kept", i)
		}
	}
	if len(keep) > 7+4+12 || len(keep) < 7+12 {
		t.Fatalf("kept %d", len(keep))
	}
	if keep["399"] || keep["200"] {
		t.Fatal("kept backups beyond retention")
	}
}

func TestPruneRemovesUnreferencedChunks(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.svc.o.Keep = Keep{Daily: 1}
	r.publishBig(t, "evt.big.blob.put", 300, 10<<10)
	old, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	// Replace all data so the old chunks are no longer referenced, then back up two days later.
	st, err := r.js.Stream(ctx, streams.Events)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	r.publishBig(t, "evt.big.blob.put", 300, 9<<10)
	r.clock = r.clock.Add(49 * time.Hour)
	r.mem.mu.Lock()
	for k := range r.mem.mod { // object ages follow the test clock
		r.mem.mod[k] = r.mem.mod[k].Add(-49 * time.Hour)
	}
	r.mem.mu.Unlock()
	cur, err := r.svc.Backup(ctx, "manual") // prunes after success
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := r.svc.Remote(ctx)
	if len(remote) != 1 || remote[0].ID != cur.ID {
		t.Fatalf("remote after prune: %+v", remote)
	}
	if _, err := r.reader().Manifest(ctx, old.ID); err == nil {
		t.Fatal("pruned backup still readable")
	}
	chunks, _ := r.mem.List(ctx, chunkPrefix)
	if len(chunks) != cur.ChunksTotal {
		t.Fatalf("%d chunks stored, %d referenced", len(chunks), cur.ChunksTotal)
	}
	list, _ := r.svc.List(ctx, 10)
	if len(list) != 2 || !list[1].Pruned {
		t.Fatalf("catalog %+v", list)
	}
	if ls, le, ok := r.svc.LastStatus(ctx); !ok || le != "" || ls.IsZero() {
		t.Fatalf("last status %v %q %v", ls, le, ok)
	}
}

func TestNextRun(t *testing.T) {
	now := time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)
	if got := nextRun(now, "03:30"); !got.Equal(time.Date(2026, 10, 24, 3, 30, 0, 0, time.UTC)) {
		t.Fatal(got)
	}
	if got := nextRun(now, "01:00"); !got.Equal(time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)) {
		t.Fatal(got)
	}
}

func TestAppendEntryMatchesServer(t *testing.T) {
	var buf bytes.Buffer
	w := archive.NewWriter(&buf)
	for _, e := range []struct {
		name     string
		ts       int64
		seq      uint64
		hdr, pay []byte
	}{{"state.json", 123, 0, nil, []byte("{}")}, {"evt.a.b.c", -5, 77, []byte("NATS/1.0\r\nA: b\r\n\r\n"), []byte("data")}, {"", 0, 0, nil, nil}} {
		if err := w.WriteHeader(&archive.Header{Name: e.name, Timestamp: e.ts, Sequence: e.seq, HeaderSize: int64(len(e.hdr)), PayloadSize: int64(len(e.pay))}); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(append(append([]byte{}, e.hdr...), e.pay...))
	}
	want := buf.Bytes()
	got := []byte(archive.MagicBytes)
	got = appendEntry(got, "state.json", 123, 0, 0, 2, []byte("{}"))
	got = appendEntry(got, "evt.a.b.c", -5, 77, 18, 4, []byte("NATS/1.0\r\nA: b\r\n\r\ndata"))
	got = appendEntry(got, "", 0, 0, 0, 0, nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("encoding differs:\n got %x\nwant %x", got, want)
	}
}
