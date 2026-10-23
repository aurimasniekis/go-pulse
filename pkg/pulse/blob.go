package pulse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// BlobsBucket is the Object Store bucket for payloads above max_payload (PL-BUS-004).
const BlobsBucket = "blobs"

// PutBlob stores r in the blobs Object Store under a new unique name (requires a blobs grant) and returns a reference
// to attach with WithBlob.
func (c *Client) PutBlob(ctx context.Context, r io.Reader, ext string) (*envelope.BlobRef, error) {
	obs, err := c.js.ObjectStore(ctx, BlobsBucket)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	name := envelope.NewID() + ext
	info, err := obs.Put(ctx, jetstream.ObjectMeta{Name: name}, io.TeeReader(r, h))
	if err != nil {
		return nil, err
	}
	return &envelope.BlobRef{Bucket: BlobsBucket, Name: name, Size: int64(info.Size), SHA256: hex.EncodeToString(h.Sum(nil))}, nil //nolint:gosec // object sizes fit int64
}

// GetBlob reads a blob and verifies its SHA-256 when the reference carries one.
func (c *Client) GetBlob(ctx context.Context, ref envelope.BlobRef) ([]byte, error) {
	obs, err := c.js.ObjectStore(ctx, ref.Bucket)
	if err != nil {
		return nil, err
	}
	b, err := obs.GetBytes(ctx, ref.Name)
	if err != nil {
		return nil, err
	}
	if ref.SHA256 != "" {
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != ref.SHA256 {
			return nil, fmt.Errorf("pulse: blob %s checksum mismatch", ref.Name)
		}
	}
	return b, nil
}
