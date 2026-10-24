//go:build interop

// Package sdktest runs the PL-SDK-005 interop check (make test-sdk-interop): Go publishes; the Python SDK and the TS SDK
// (node over tls:// and over ws://, the browser transport) replay with since and must see identical events, follow a
// durable, read a blob, and publish events and blobs that Go reads back.
package sdktest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
	"github.com/aurimasniekis/go-pulse/pkg/pulse"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

func evJSON(e pulse.Event) map[string]any {
	var tm any
	if !e.Time.IsZero() {
		tm = e.Time.UTC().Format(time.RFC3339Nano)
	}
	extra := map[string]any{}
	for k, vs := range e.Extra {
		l := make([]any, len(vs))
		for i, v := range vs {
			l[i] = v
		}
		extra[k] = l
	}
	var blob any
	if e.Blob != nil {
		blob = map[string]any{"bucket": e.Blob.Bucket, "name": e.Blob.Name, "size": float64(e.Blob.Size), "sha256": e.Blob.SHA256}
	}
	return map[string]any{"seq": float64(e.Sequence), "subject": e.Subject, "id": e.ID, "type": e.Type, "source": e.Source, "time": tm,
		"content_type": e.ContentType, "data_b64": base64.StdEncoding.EncodeToString(e.Data), "extra": extra, "blob": blob,
		"stream_time_ns": strconv.FormatInt(e.StreamTime.UnixNano(), 10)}
}

type result struct {
	Events     []map[string]any `json:"events"`
	BlobSHA256 string           `json:"blob_sha256"`
	Durable    []uint64         `json:"durable"`
	Published  struct {
		Seq  uint64           `json:"seq"`
		ID   string           `json:"id"`
		Blob envelope.BlobRef `json:"blob"`
	} `json:"published"`
}

func run(t *testing.T, name string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, stderr.String())
	}
	t.Logf("%s stderr:\n%s", name, stderr.String())
	var r result
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("%s: bad output %v\n%s", name, err, out)
	}
	return r
}

func TestInterop(t *testing.T) {
	root, _ := filepath.Abs("../..")
	py := filepath.Join(root, "sdk/python/.venv/bin/python")
	if _, err := os.Stat(py); err != nil {
		t.Skip("sdk/python/.venv missing: run make test-sdk-interop")
	}
	if _, err := os.Stat(filepath.Join(root, "sdk/ts/dist/index.js")); err != nil {
		t.Skip("sdk/ts/dist missing: run make test-sdk-interop")
	}
	ctx := context.Background()
	env := natstest.Start(t)
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := clients.New(db, env.Keys, sysapi.New(env.SysConn()), nil)
	if _, err := svc.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	ajs, _ := jetstream.New(env.AppConn())
	if _, err := streams.EnsureEvents(ctx, ajs, 256<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := streams.EnsureBlobs(ctx, ajs); err != nil {
		t.Fatal(err)
	}
	grants := []natsauth.Grant{{Action: natsauth.ActPublish, Subject: "evt.interop.>"}, {Action: natsauth.ActSubscribe, Subject: "evt.interop.>"},
		{Action: natsauth.ActBlobs}}
	for _, n := range []string{"py", "ts", "tsws"} {
		grants = append(grants, natsauth.Grant{Action: natsauth.ActDurable, Subject: "evt.interop.>", Name: n})
	}
	_, creds, err := svc.Create(ctx, clients.CreateInput{Name: "interop", Grants: grants}, audit.Actor{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	credsFile, caFile := filepath.Join(dir, "interop.creds"), filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(credsFile, creds, 0o600)
	_ = os.WriteFile(caFile, []byte(env.Keys.CACert), 0o600)

	gc, err := pulse.Connect(env.ClientURL, pulse.WithCreds(creds), pulse.WithCA([]byte(env.Keys.CACert)), pulse.WithSource("go-interop"))
	if err != nil {
		t.Fatal(err)
	}
	defer gc.Close()
	since := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	big := bytes.Repeat([]byte("go blob payload "), 20000) // 320 KB: three object chunks
	ref, err := gc.PutBlob(ctx, bytes.NewReader(big), ".bin")
	if err != nil {
		t.Fatal(err)
	}
	pubs := []func() error{
		func() error {
			_, err := gc.Publish(ctx, "evt.interop.door.opened", "interop.door.opened.v1", map[string]any{"door": "front", "n": 1})
			return err
		},
		func() error {
			_, err := gc.Publish(ctx, "evt.interop.note.added", "interop.note.v1", "plain text ✓")
			return err
		},
		func() error {
			_, err := gc.Publish(ctx, "evt.interop.bytes.sent", "interop.bytes.v1", []byte{0, 1, 2, 255})
			return err
		},
		func() error {
			_, err := gc.JetStream().Publish(ctx, "evt.interop.raw.thing", []byte("raw"))
			return err
		},
		func() error {
			_, err := gc.Publish(ctx, "evt.interop.trace.seen", "interop.trace.v1", map[string]any{}, pulse.WithHeader("x-trace", "abc"),
				pulse.WithTime(time.Date(2026, 10, 23, 10, 11, 12, 123456789, time.UTC)))
			return err
		},
		func() error {
			_, err := gc.Publish(ctx, "evt.interop.blob.stored", "interop.blob.v1", nil, pulse.WithBlob(*ref))
			return err
		},
	}
	for _, p := range pubs {
		if err := p(); err != nil {
			t.Fatal(err)
		}
	}
	// What Go reads back is the reference.
	got := make(chan pulse.Event, 16)
	sub, err := gc.Subscribe(ctx, "evt.interop.>", func(_ context.Context, e pulse.Event) error { got <- e; return nil }, pulse.Since(time.Now().Add(-time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	var want []map[string]any
	for range pubs {
		select {
		case e := <-got:
			want = append(want, evJSON(e))
		case <-time.After(10 * time.Second):
			t.Fatal("go replay timed out")
		}
	}
	sub.Stop()
	sum := sha256.Sum256(big)
	wantSHA := hex.EncodeToString(sum[:])
	normalize := func(v any) any { b, _ := json.Marshal(v); var x any; _ = json.Unmarshal(b, &x); return x }

	n := strconv.Itoa(len(pubs))
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d", env.WSPort)
	runs := map[string][]string{
		"python":  {py, filepath.Join(root, "sdk/python/tests/interop.py"), "--url", env.ClientURL, "--creds", credsFile, "--ca", caFile, "--since", since, "--count", n},
		"ts-node": {"node", filepath.Join(root, "sdk/ts/test/interop.mjs"), "--url", env.ClientURL, "--creds", credsFile, "--ca", caFile, "--since", since, "--count", n, "--durable", "ts"},
		"ts-ws":   {"node", filepath.Join(root, "sdk/ts/test/interop.mjs"), "--url", wsURL, "--creds", credsFile, "--since", since, "--count", n, "--durable", "tsws"},
	}
	for _, name := range []string{"python", "ts-node", "ts-ws"} {
		t.Run(name, func(t *testing.T) {
			r := run(t, name, runs[name]...)
			if !reflect.DeepEqual(normalize(r.Events), normalize(want)) {
				a, _ := json.MarshalIndent(r.Events, "", " ")
				b, _ := json.MarshalIndent(want, "", " ")
				t.Fatalf("events differ\n got %s\nwant %s", a, b)
			}
			if r.BlobSHA256 != wantSHA {
				t.Fatalf("blob sha %s, want %s", r.BlobSHA256, wantSHA)
			}
			seq := func(i int) uint64 { return uint64(want[i]["seq"].(float64)) }
			if !reflect.DeepEqual(r.Durable, []uint64{seq(0), seq(1), seq(2)}) {
				t.Fatalf("durable resume %v", r.Durable)
			}
			// Go reads what the SDK published, including its blob.
			// Read with pulsed's own connection: scoped clients may not direct-get by sequence.
			m, err := ajs.Stream(ctx, streams.Events)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := m.GetMsg(ctx, r.Published.Seq)
			if err != nil {
				t.Fatal(err)
			}
			e := envelope.Decode(raw.Subject, raw.Header, raw.Data)
			if e.Type != "interop.said.v1" || e.ID != r.Published.ID || e.Blob == nil || *e.Blob != r.Published.Blob || len(e.Extra["x-lang"]) != 1 {
				t.Fatalf("published event %+v", e)
			}
			b, err := gc.GetBlob(ctx, *e.Blob)
			if err != nil {
				t.Fatalf("go reads %s blob: %v", name, err)
			}
			if int64(len(b)) != e.Blob.Size || len(b) < 200_000 {
				t.Fatalf("blob size %d", len(b))
			}
		})
	}
}
