package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/secretbox"
)

// ErrTampered means a manifest or chunk does not match its authenticated name.
var ErrTampered = errors.New("backup: object does not match its reference (modified or substituted)")

// Reader reads backups with an age identity. NameKey (from the master key) authenticates manifests and chunks; without
// it, restore relies on age alone.
type Reader struct {
	Store      ObjectStore
	Identities []age.Identity
	NameKey    []byte
}

// Manifest fetches, authenticates and decrypts a backup's manifest.
func (r *Reader) Manifest(ctx context.Context, id string) (*Manifest, error) {
	rb, err := r.Store.Get(ctx, refsKey(id))
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("backup %s: not found or incomplete", id)
	}
	if err != nil {
		return nil, err
	}
	var refs Refs
	if err := json.Unmarshal(rb, &refs); err != nil {
		return nil, err
	}
	ct, err := r.Store.Get(ctx, manifestKey(id))
	if err != nil {
		return nil, fmt.Errorf("backup %s manifest: %w", id, err)
	}
	pt, err := open(r.Identities, ct)
	if err != nil {
		return nil, err
	}
	if len(r.NameKey) > 0 && chunkName(r.NameKey, pt) != refs.ManifestMAC {
		return nil, fmt.Errorf("manifest: %w", ErrTampered)
	}
	var m Manifest
	if err := json.Unmarshal(pt, &m); err != nil {
		return nil, err
	}
	if m.ID != id {
		return nil, fmt.Errorf("manifest: %w", ErrTampered)
	}
	return &m, nil
}

func (r *Reader) chunk(ctx context.Context, b Bucket) ([]byte, error) {
	ct, err := r.Store.Get(ctx, chunkPrefix+b.Chunk)
	if err != nil {
		return nil, fmt.Errorf("chunk %s: %w", b.Chunk[:12], err)
	}
	data, err := open(r.Identities, ct)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != b.Size || (len(r.NameKey) > 0 && chunkName(r.NameKey, data) != b.Chunk) {
		return nil, fmt.Errorf("chunk %s: %w", b.Chunk[:12], ErrTampered)
	}
	return data, nil
}

// File decrypts one whole-file object (pulse.db, keys.enc) and checks its digest.
func (r *Reader) File(ctx context.Context, m *Manifest, name string) ([]byte, error) {
	for _, f := range m.Files {
		if f.Name != name {
			continue
		}
		ct, err := r.Store.Get(ctx, f.Object)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		b, err := open(r.Identities, ct)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return nil, fmt.Errorf("%s: %w", name, ErrTampered)
		}
		return b, nil
	}
	return nil, fmt.Errorf("backup has no %s", name)
}

// WriteFile restores a whole-file object to path (atomically; refuses to overwrite unless overwrite is set).
func (r *Reader) WriteFile(ctx context.Context, m *Manifest, name, path string, overwrite bool) error {
	if _, err := os.Stat(path); err == nil && !overwrite {
		return fmt.Errorf("%s exists; pass overwrite to replace it", path)
	}
	b, err := r.File(ctx, m, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return secretbox.WriteFileAtomic(path, b, 0o600)
}

// StreamPlan says what restoring a stream would do.
type StreamPlan struct {
	Name     string `json:"name"`
	Msgs     uint64 `json:"msgs"`
	Bytes    uint64 `json:"bytes"`
	FirstSeq uint64 `json:"first_seq"`
	LastSeq  uint64 `json:"last_seq"`
	Exists   bool   `json:"exists" doc:"A stream with this name exists now; restoring needs replace"`
	Action   string `json:"action" enum:"restore,replace,skip" doc:"What a restore with the given options does"`
	Result   string `json:"result,omitempty" doc:"After a restore: restored, skipped or the error"`
}

// RestoreOptions select what to restore.
type RestoreOptions struct {
	Streams []string // empty = all streams in the backup
	Replace bool     // delete existing streams first
	DryRun  bool
}

// RestoreStreams restores streams from manifest m over nc (PL-BAK-006). Existing streams are skipped unless Replace.
func (r *Reader) RestoreStreams(ctx context.Context, nc *nats.Conn, js jetstream.JetStream, m *Manifest, o RestoreOptions) ([]StreamPlan, error) {
	want := map[string]bool{}
	for _, n := range o.Streams {
		want[n] = true
	}
	missing := maps.Clone(want)
	var out []StreamPlan
	for _, sb := range m.Streams {
		if len(want) > 0 && !want[sb.Name] {
			continue
		}
		delete(missing, sb.Name)
		p := StreamPlan{Name: sb.Name, Msgs: sb.Msgs, Bytes: sb.Bytes, FirstSeq: sb.FirstSeq, LastSeq: sb.LastSeq, Action: "restore"}
		if _, err := js.Stream(ctx, sb.Name); err == nil {
			p.Exists = true
			p.Action = "skip"
			if o.Replace {
				p.Action = "replace"
			}
		} else if !errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, err
		}
		out = append(out, p)
	}
	for n := range missing {
		return nil, fmt.Errorf("backup has no stream %s", n)
	}
	if o.DryRun {
		return out, nil
	}
	for i := range out {
		p := &out[i]
		if p.Action == "skip" {
			p.Result = "skipped (exists)"
			continue
		}
		if err := r.restoreStream(ctx, nc, js, m, p.Name, p.Action == "replace"); err != nil {
			p.Result = err.Error()
			return out, err
		}
		p.Result = "restored"
	}
	return out, nil
}

func (r *Reader) restoreStream(ctx context.Context, nc *nats.Conn, js jetstream.JetStream, m *Manifest, name string, replace bool) error {
	var sb *StreamBackup
	for i := range m.Streams {
		if m.Streams[i].Name == name {
			sb = &m.Streams[i]
		}
	}
	if sb == nil {
		return fmt.Errorf("backup has no stream %s", name)
	}
	if replace {
		// Fetch everything first: a damaged or substituted chunk must not cost the existing stream.
		if err := r.check(ctx, sb); err != nil {
			return err
		}
		if err := js.DeleteStream(ctx, name); err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("delete %s: %w", name, err)
		}
	}
	pr, pw := io.Pipe()
	go func() { _ = pw.CloseWithError(buildSnapshot(ctx, pw, *sb, r.chunk)) }()
	err := Restore(ctx, nc, name, sb.Meta, pr)
	_ = pr.CloseWithError(err)
	return err
}

// check fetches and authenticates every chunk of a stream without keeping them.
func (r *Reader) check(ctx context.Context, sb *StreamBackup) error {
	for _, b := range sb.Buckets {
		if _, err := r.chunk(ctx, b); err != nil {
			return fmt.Errorf("%s sequences %d–%d: %w", sb.Name, b.FirstSeq, b.LastSeq, err)
		}
	}
	return nil
}
