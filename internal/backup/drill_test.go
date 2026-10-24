package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

type drillEnv struct {
	nc *nats.Conn
	js jetstream.JetStream
}

func newDrillEnv(t *testing.T) *drillEnv {
	t.Helper()
	env := natstest.Start(t, natstest.Options{NoWebsocket: true})
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := clients.New(db, env.Keys, sysapi.New(env.SysConn()), nil).Sync(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	nc := env.AppConn()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := streams.EnsureEvents(context.Background(), js, 256<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	return &drillEnv{nc: nc, js: js}
}

type stored struct {
	seq  uint64
	ts   time.Time
	subj string
	data string
	id   string
}

// publishBatches writes three batches about a second apart so stream timestamps differ measurably.
func (d *drillEnv) publishBatches(t *testing.T) (all []stored, batchB time.Time) {
	t.Helper()
	ctx := context.Background()
	for b := range 3 {
		if b > 0 {
			time.Sleep(1100 * time.Millisecond)
		}
		for i := range 5 {
			m, err := envelope.Encode(envelope.Event{Subject: fmt.Sprintf("evt.drill.batch%d.item", b), Type: "drill.item.v1", Source: "drill",
				Data: fmt.Appendf(nil, `{"b":%d,"i":%d}`, b, i)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.js.PublishMsg(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	return d.read(t, streams.Events), time.Time{}
}

func (d *drillEnv) read(t *testing.T, stream string) []stored {
	t.Helper()
	s, err := d.js.Stream(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := s.Info(context.Background())
	var out []stored
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		m, err := s.GetMsg(context.Background(), seq)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, stored{seq: m.Sequence, ts: m.Time, subj: m.Subject, data: string(m.Data), id: m.Header.Get(envelope.HeaderID)})
	}
	return out
}

func byStartTime(t *testing.T, js jetstream.JetStream, stream string, start time.Time, want int) []uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := js.OrderedConsumer(ctx, stream, jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverByStartTimePolicy, OptStartTime: &start})
	if err != nil {
		t.Fatal(err)
	}
	var seqs []uint64
	for len(seqs) < want {
		m, err := c.Next(jetstream.FetchMaxWait(2 * time.Second))
		if err != nil {
			break
		}
		md, _ := m.Metadata()
		seqs = append(seqs, md.Sequence.Stream)
	}
	return seqs
}

// TestRestoreDrill is the PL-BAK-007 release gate (RG-4).
//
//  1. Republishing exported events into a stream cannot keep their timestamps: NATS stamps every publish with the time
//     it is stored. Restores therefore go through the JetStream restore API.
//  2. The restore API accepts a NATSARC1 archive and stores every record with its archived sequence and timestamp
//     (server snapshot round trip, durable consumer included).
//  3. pulsed's backups (NATSARC1 records in deduplicated buckets, ADR-0035) restore to the same events, sequences and
//     timestamps, so DeliverByStartTime replay is unchanged, both for the latest backup and for an older one.
func TestRestoreDrill(t *testing.T) {
	r := newRig(t)
	d := r.drillEnv
	ctx := context.Background()
	orig, _ := d.publishBatches(t)
	if len(orig) != 15 {
		t.Fatalf("published %d", len(orig))
	}
	startB := orig[5].ts // first event of the second batch
	if !startB.After(orig[4].ts.Add(time.Second)) {
		t.Fatalf("batches not spread in time: %v %v", orig[4].ts, startB)
	}
	if got := byStartTime(t, d.js, streams.Events, startB, 10); len(got) != 10 || got[0] != orig[5].seq {
		t.Fatalf("baseline by-start-time replay: %v", got)
	}

	// 1. Republish.
	if _, err := d.js.CreateStream(ctx, jetstream.StreamConfig{Name: "REPUB", Subjects: []string{"repub.>"}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range orig {
		m := nats.NewMsg("repub." + e.subj)
		m.Header.Set(envelope.HeaderID, e.id)
		m.Data = []byte(e.data)
		if _, err := d.js.PublishMsg(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	repub := d.read(t, "REPUB")
	kept := 0
	for i := range repub {
		if repub[i].ts.Equal(orig[i].ts) {
			kept++
		}
	}
	if kept == len(orig) {
		t.Fatal("republished events kept their timestamps: NATS changed behaviour, revisit ADR-0035")
	}
	t.Logf("republish: %d/%d timestamps preserved (expected none)", kept, len(orig))
	if err := d.js.DeleteStream(ctx, "REPUB"); err != nil {
		t.Fatal(err)
	}

	// A durable consumer with progress must survive every round trip.
	cons, err := d.js.CreateOrUpdateConsumer(ctx, streams.Events, jetstream.ConsumerConfig{Durable: "drill_reader", AckPolicy: jetstream.AckExplicitPolicy})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(4, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for m := range batch.Messages() {
		if err := m.DoubleAck(actx); err != nil {
			t.Fatal(err)
		}
	}
	checkRestored := func(t *testing.T, want []stored) {
		t.Helper()
		got := d.read(t, streams.Events)
		if len(got) != len(want) {
			t.Fatalf("restored %d of %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("event %d differs after restore:\n got %+v\nwant %+v", i, got[i], want[i])
			}
		}
		if seqs := byStartTime(t, d.js, streams.Events, startB, len(want)-5); len(seqs) != len(want)-5 || seqs[0] != orig[5].seq {
			t.Fatalf("by-start-time replay after restore: %v", seqs)
		}
		ci, err := d.js.Consumer(ctx, streams.Events, "drill_reader")
		if err != nil {
			t.Fatalf("durable consumer lost: %v", err)
		}
		if info, _ := ci.Info(ctx); info.AckFloor.Stream != orig[3].seq {
			t.Fatalf("durable ack floor %d, want %d", info.AckFloor.Stream, orig[3].seq)
		}
	}

	// 2. Server snapshot round trip.
	var buf bytes.Buffer
	meta, err := Snapshot(ctx, d.nc, streams.Events, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, d.nc, streams.Events, *meta, bytes.NewReader(buf.Bytes())); !errors.Is(err, ErrStreamExists) {
		t.Fatalf("restore over an existing stream: %v", err)
	}
	if err := d.js.DeleteStream(ctx, streams.Events); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, d.nc, streams.Events, *meta, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	checkRestored(t, orig)

	// 3. pulsed backups: one now, more events, another one; restore both.
	first, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	for i := range 3 {
		m, _ := envelope.Encode(envelope.Event{Subject: "evt.drill.batch3.item", Type: "drill.item.v1", Data: fmt.Appendf(nil, `{"i":%d}`, i)})
		if _, err := d.js.PublishMsg(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	later := d.read(t, streams.Events)
	r.clock = r.clock.AddDate(0, 0, 1) // next day: GFS keeps both
	second, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   string
		want []stored
	}{{second.ID, later}, {first.ID, orig}} {
		m, err := r.reader().Manifest(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.reader().RestoreStreams(ctx, d.nc, d.js, m, RestoreOptions{Streams: []string{streams.Events}, Replace: true}); err != nil {
			t.Fatal(err)
		}
		checkRestored(t, c.want)
	}
	// New events continue after the restored sequence.
	m, _ := envelope.Encode(envelope.Event{Subject: "evt.drill.after.restore", Data: []byte("{}")})
	ack, err := d.js.PublishMsg(ctx, m)
	if err != nil || ack.Sequence != orig[14].seq+1 {
		t.Fatalf("publish after restore: %v seq %d", err, ack.Sequence)
	}
}
