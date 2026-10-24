// Package streams ensures and manages JetStream streams in the APP account (PL-BUS-001, PL-BUS-004/005/006).
package streams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/natsauth"
)

// Events is the durable event stream.
const (
	Events             = "EVENTS"
	EventsSubject      = "evt.>"
	DuplicateWindow    = 2 * time.Minute
	MaxMsgSize         = 1 << 20
	EventsMaxConsumers = 256
)

// EventsConfig is the desired EVENTS configuration (ADR-0003).
func EventsConfig(maxBytes int64) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:              Events,
		Description:       "Pulse durable events (managed by pulsed)",
		Subjects:          []string{EventsSubject},
		Retention:         jetstream.LimitsPolicy,
		Storage:           jetstream.FileStorage,
		MaxAge:            0,
		MaxMsgs:           -1,
		MaxBytes:          maxBytes,
		MaxMsgSize:        MaxMsgSize,
		MaxMsgsPerSubject: -1,
		Discard:           jetstream.DiscardOld,
		Compression:       jetstream.S2Compression,
		Duplicates:        DuplicateWindow,
		AllowDirect:       true,
		Replicas:          1,
		MaxConsumers:      EventsMaxConsumers,
	}
}

// EnsureResult reports what EnsureEvents did.
type EnsureResult struct {
	Created  bool
	Updated  bool
	Info     *jetstream.StreamInfo
	Warnings []string
}

// EnsureEvents creates EVENTS or corrects drift in the managed fields. A max_bytes decrease deletes old data and is
// applied only with allowShrink (PL-BUS-001); otherwise the current max_bytes is kept and a warning is returned.
func EnsureEvents(ctx context.Context, js jetstream.JetStream, maxBytes int64, allowShrink bool, log *slog.Logger) (*EnsureResult, error) {
	if log == nil {
		log = slog.Default()
	}
	want := EventsConfig(maxBytes)
	res := &EnsureResult{}
	s, err := js.Stream(ctx, Events)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		s, err = js.CreateStream(ctx, want)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", Events, err)
		}
		res.Created = true
		res.Info = s.CachedInfo()
		log.Info("created stream", "stream", Events, "max_bytes", maxBytes)
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	cur := s.CachedInfo().Config
	if cur.Storage != want.Storage {
		return nil, fmt.Errorf("%s uses %s storage; pulsed requires file storage (storage cannot be changed in place)", Events, cur.Storage)
	}
	if want.MaxBytes < cur.MaxBytes && cur.MaxBytes > 0 && !allowShrink {
		res.Warnings = append(res.Warnings, fmt.Sprintf("configured max_bytes %d is below the current %d; keeping %d (shrinking deletes "+
			"old events and pulsed never does it on its own; to shrink, take a backup and use the nats CLI as the APP account)", want.MaxBytes, cur.MaxBytes, cur.MaxBytes))
		want.MaxBytes = cur.MaxBytes
	}
	upd := cur
	upd.Description = want.Description
	upd.Subjects = want.Subjects
	upd.Retention = want.Retention
	upd.MaxAge = want.MaxAge
	upd.MaxMsgs = want.MaxMsgs
	upd.MaxBytes = want.MaxBytes
	upd.MaxMsgSize = want.MaxMsgSize
	upd.MaxMsgsPerSubject = want.MaxMsgsPerSubject
	upd.Discard = want.Discard
	upd.Compression = want.Compression
	upd.Duplicates = want.Duplicates
	upd.AllowDirect = want.AllowDirect
	// max_consumers is set at creation only: NATS refuses to change it on an existing stream.
	if configEqual(cur, upd) {
		res.Info = s.CachedInfo()
		return res, nil
	}
	s, err = js.UpdateStream(ctx, upd)
	if err != nil {
		return nil, fmt.Errorf("update %s: %w", Events, err)
	}
	res.Updated = true
	res.Info = s.CachedInfo()
	log.Info("corrected stream drift", "stream", Events)
	return res, nil
}

func configEqual(a, b jetstream.StreamConfig) bool {
	return a.Description == b.Description && slices.Equal(a.Subjects, b.Subjects) && a.Retention == b.Retention &&
		a.MaxAge == b.MaxAge && a.MaxMsgs == b.MaxMsgs && a.MaxBytes == b.MaxBytes && a.MaxMsgSize == b.MaxMsgSize &&
		a.MaxMsgsPerSubject == b.MaxMsgsPerSubject && a.Discard == b.Discard && a.Compression == b.Compression &&
		a.Duplicates == b.Duplicates && a.AllowDirect == b.AllowDirect && a.MaxConsumers == b.MaxConsumers
}

// EnsureBlobs creates the blobs Object Store if it does not exist (PL-BUS-004).
func EnsureBlobs(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.ObjectStore(ctx, natsauth.BlobsBucket)
	if err == nil {
		return nil
	}
	if !errors.Is(err, jetstream.ErrBucketNotFound) {
		return err
	}
	_, err = js.CreateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:      natsauth.BlobsBucket,
		Description: "Pulse large payloads referenced by events (pulse-blob header)",
		Storage:     jetstream.FileStorage,
		Compression: true,
	})
	return err
}
