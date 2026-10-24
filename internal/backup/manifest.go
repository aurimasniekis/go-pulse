package backup

import (
	"context"
	"encoding/binary"

	"fmt"
	"io"
	"strings"
	"time"

	"github.com/klauspost/compress/s2"
	"github.com/nats-io/nats-server/v2/server/archive"
)

// Object layout under the store prefix.
const (
	chunkPrefix  = "chunks/"
	backupPrefix = "backups/"
	// Message records are grouped into buckets anchored to fixed windows of sequence numbers, so a bucket whose
	// messages have not changed serialises to the same bytes every night and is stored once (ADR-0035).
	bucketWindow = 1024
	bucketMax    = 16 << 20 // a window with large messages is split further, at deterministic record boundaries
)

func manifestKey(id string) string { return backupPrefix + id + "/manifest.age" }
func refsKey(id string) string     { return backupPrefix + id + "/refs.json" }
func fileKey(id, name string) string {
	return backupPrefix + id + "/" + name + ".age"
}

// Manifest describes one backup; it is stored encrypted.
type Manifest struct {
	Version       int            `json:"v"`
	ID            string         `json:"id"`
	Created       time.Time      `json:"created"`
	PulsedVersion string         `json:"pulsed_version"`
	Files         []FileRef      `json:"files"`
	Streams       []StreamBackup `json:"streams"`
}

// FileRef is a whole file stored as one encrypted object (pulse.db, keys.enc).
type FileRef struct {
	Name   string `json:"name"`
	Object string `json:"object"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// StreamBackup is one stream snapshot (NATSARC1): the state and consumer records inline, message records in buckets.
type StreamBackup struct {
	Name      string         `json:"name"`
	Meta      StreamMeta     `json:"meta"`
	Msgs      uint64         `json:"msgs"`
	Bytes     uint64         `json:"bytes"`
	FirstSeq  uint64         `json:"first_seq"`
	LastSeq   uint64         `json:"last_seq"`
	State     ArchiveEntry   `json:"state"`
	Consumers []ArchiveEntry `json:"consumers"`
	Buckets   []Bucket       `json:"buckets"`
}

// ArchiveEntry is one non-message archive entry (state.json, consumers/<name>).
type ArchiveEntry struct {
	Name    string `json:"name"`
	Time    int64  `json:"ts"`
	Payload []byte `json:"payload"`
}

// Bucket references a content-addressed chunk of serialised message records.
type Bucket struct {
	Chunk    string `json:"chunk"`
	Size     int64  `json:"size"`
	FirstSeq uint64 `json:"first_seq"`
	LastSeq  uint64 `json:"last_seq"`
	Msgs     int    `json:"msgs"`
}

// Refs is the plaintext companion of a manifest: what the backup references, for listing, pruning and garbage
// collection without the age identity. It holds no names or content. It is written last and marks a complete backup.
type Refs struct {
	ID           string    `json:"id"`
	Created      time.Time `json:"created"`
	LogicalBytes int64     `json:"logical_bytes"`
	Chunks       []string  `json:"chunks"`
	Objects      []string  `json:"objects"`
	// ManifestMAC authenticates the manifest plaintext with the chunk-naming key: age recipients are public, so
	// anyone with write access to the bucket could otherwise substitute a manifest encrypted to them.
	ManifestMAC string `json:"manifest_mac"`
}

// appendEntry serialises one archive entry exactly as the server's archive.Writer does (without the stream magic).
func appendEntry(b []byte, name string, ts int64, seq uint64, hdrSize, paySize int64, payload []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(name)))
	b = binary.AppendVarint(b, ts)
	b = binary.AppendUvarint(b, seq)
	b = binary.AppendUvarint(b, uint64(hdrSize)) //nolint:gosec // sizes come from the archive and are non-negative
	b = binary.AppendUvarint(b, uint64(paySize)) //nolint:gosec // as above
	b = append(b, name...)
	return append(b, payload...)
}

// chunker uploads buckets that are not stored yet.
type chunker struct {
	ctx     context.Context
	store   ObjectStore
	enc     func([]byte) ([]byte, error)
	key     []byte
	known   map[string]bool
	newN    int
	newSize int64
	total   int
}

func (c *chunker) put(data []byte) (string, error) {
	name := chunkName(c.key, data)
	c.total++
	if !c.known[name] {
		ct, err := c.enc(data)
		if err != nil {
			return "", err
		}
		if err := c.store.Put(c.ctx, chunkPrefix+name, ct); err != nil {
			return "", fmt.Errorf("upload chunk: %w", err)
		}
		c.known[name] = true
		c.newN++
		c.newSize += int64(len(ct))
	}
	return name, nil
}

// splitSnapshot reads a server snapshot (NATSARC1 inside S2) into sb, storing message records in buckets.
func splitSnapshot(r io.Reader, sb *StreamBackup, c *chunker) error {
	ar := archive.NewReader(s2.NewReader(r))
	h, err := ar.Next()
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	if h.Name != "state.json" {
		return fmt.Errorf("snapshot does not start with state.json (got %q)", h.Name)
	}
	payload, err := io.ReadAll(ar)
	if err != nil {
		return err
	}
	sb.State = ArchiveEntry{Name: h.Name, Time: h.Timestamp, Payload: payload}
	sb.Consumers = []ArchiveEntry{}
	var cur Bucket
	var buf []byte
	window := uint64(0)
	flush := func() error {
		if cur.Msgs == 0 {
			return nil
		}
		name, err := c.put(buf)
		if err != nil {
			return err
		}
		cur.Chunk, cur.Size = name, int64(len(buf))
		sb.Buckets = append(sb.Buckets, cur)
		cur, buf = Bucket{}, buf[:0]
		return nil
	}
	for {
		h, err := ar.Next()
		if err != nil {
			return fmt.Errorf("read snapshot: %w", err)
		}
		if h.Sequence == 0 {
			if h.Name == "" && h.Timestamp == 0 && h.HeaderSize == 0 && h.PayloadSize == 0 {
				return flush() // end-of-backup sentinel
			}
			if !strings.HasPrefix(h.Name, "consumers/") {
				return fmt.Errorf("snapshot: unexpected entry %q", h.Name)
			}
			p, err := io.ReadAll(ar)
			if err != nil {
				return err
			}
			sb.Consumers = append(sb.Consumers, ArchiveEntry{Name: h.Name, Time: h.Timestamp, Payload: p})
			continue
		}
		if w := h.Sequence / bucketWindow; cur.Msgs > 0 && (w != window || len(buf) >= bucketMax) {
			if err := flush(); err != nil {
				return err
			}
		}
		window = h.Sequence / bucketWindow
		p, err := io.ReadAll(ar)
		if err != nil {
			return err
		}
		if int64(len(p)) != h.HeaderSize+h.PayloadSize {
			return fmt.Errorf("snapshot: short record for sequence %d", h.Sequence)
		}
		buf = appendEntry(buf, h.Name, h.Timestamp, h.Sequence, h.HeaderSize, h.PayloadSize, p)
		if cur.Msgs == 0 {
			cur.FirstSeq = h.Sequence
		}
		cur.LastSeq = h.Sequence
		cur.Msgs++
	}
}

// buildSnapshot writes the stream's archive (NATSARC1 inside S2) from the manifest, fetching and checking buckets.
func buildSnapshot(ctx context.Context, w io.Writer, sb StreamBackup, fetch func(ctx context.Context, b Bucket) ([]byte, error)) error {
	enc := s2.NewWriter(w)
	head := append([]byte(archive.MagicBytes), appendEntry(nil, sb.State.Name, sb.State.Time, 0, 0, int64(len(sb.State.Payload)), sb.State.Payload)...)
	for _, c := range sb.Consumers {
		head = appendEntry(head, c.Name, c.Time, 0, 0, int64(len(c.Payload)), c.Payload)
	}
	if _, err := enc.Write(head); err != nil {
		return err
	}
	for _, b := range sb.Buckets {
		data, err := fetch(ctx, b)
		if err != nil {
			return fmt.Errorf("sequences %d–%d: %w", b.FirstSeq, b.LastSeq, err)
		}
		if _, err := enc.Write(data); err != nil {
			return err
		}
	}
	if _, err := enc.Write(appendEntry(nil, "", 0, 0, 0, 0, nil)); err != nil { // end-of-backup sentinel
		return err
	}
	return enc.Close()
}
