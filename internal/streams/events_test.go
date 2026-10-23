package streams

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

// appJS boots a server, pushes the APP account and returns a JetStream context for pulsed's APP user.
func appJS(t *testing.T) jetstream.JetStream {
	t.Helper()
	env := natstest.Start(t)
	db, err := store.Open(context.Background(), store.Options{Path: env.Layout.DatabaseFile()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := clients.New(db, env.Keys, sysapi.New(env.SysConn()), nil).Sync(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	js, err := jetstream.New(env.AppConn())
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func TestEnsureEvents(t *testing.T) {
	js := appJS(t)
	ctx := context.Background()
	r, err := EnsureEvents(ctx, js, 1<<30, false, nil)
	if err != nil || !r.Created {
		t.Fatalf("create: %+v %v", r, err)
	}
	c := r.Info.Config
	if c.Compression != jetstream.S2Compression || !c.AllowDirect || c.Duplicates != 2*time.Minute || c.Discard != jetstream.DiscardOld ||
		c.MaxAge != 0 || c.MaxBytes != 1<<30 || c.Storage != jetstream.FileStorage {
		t.Fatalf("config %+v", c)
	}
	if r, err = EnsureEvents(ctx, js, 1<<30, false, nil); err != nil || r.Created || r.Updated {
		t.Fatalf("second ensure: %+v %v", r, err)
	}

	// Drift (someone changed the duplicate window) is corrected.
	c.Duplicates = time.Minute
	if _, err := js.UpdateStream(ctx, c); err != nil {
		t.Fatal(err)
	}
	if r, err = EnsureEvents(ctx, js, 1<<30, false, nil); err != nil || !r.Updated || r.Info.Config.Duplicates != 2*time.Minute {
		t.Fatalf("drift: %+v %v", r, err)
	}
	// Growing is applied; shrinking is refused without confirmation, applied with it.
	if r, err = EnsureEvents(ctx, js, 2<<30, false, nil); err != nil || r.Info.Config.MaxBytes != 2<<30 {
		t.Fatalf("grow: %v", err)
	}
	if r, err = EnsureEvents(ctx, js, 1<<30, false, nil); err != nil || r.Info.Config.MaxBytes != 2<<30 || len(r.Warnings) != 1 {
		t.Fatalf("shrink without confirmation: %+v %v", r, err)
	}
	if r, err = EnsureEvents(ctx, js, 1<<30, true, nil); err != nil || r.Info.Config.MaxBytes != 1<<30 {
		t.Fatalf("confirmed shrink: %v", err)
	}
}

func TestEnsureBlobs(t *testing.T) {
	js := appJS(t)
	ctx := context.Background()
	for range 2 {
		if err := EnsureBlobs(ctx, js); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := js.ObjectStore(ctx, "blobs"); err != nil {
		t.Fatal(err)
	}
}
