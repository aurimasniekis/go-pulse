package query

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

type stored struct {
	seq     uint64
	subject string
	typ     string
	color   string
	data    string
}

// fixture: 600 events over 3 subjects; every 7th is untyped; payload {"n":i,"color":...}; the time mark sits before #400.
func fixture(t *testing.T) (jetstream.JetStream, []stored, time.Time) {
	t.Helper()
	env := natstest.Start(t)
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: env.Layout.DatabaseFile()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := clients.New(db, env.Keys, sysapi.New(env.SysConn()), nil).Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	js, _ := jetstream.New(env.AppConn())
	if _, err := streams.EnsureEvents(ctx, js, 256<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	subjects := []string{"evt.a.x.created", "evt.b.y.updated", "evt.c.z.deleted"}
	colors := []string{"red", "green", "blue", "black"}
	var all []stored
	var mark time.Time
	for i := range 600 {
		if i == 400 {
			time.Sleep(20 * time.Millisecond)
			mark = time.Now()
			time.Sleep(20 * time.Millisecond)
		}
		s := stored{subject: subjects[i%3], color: colors[i%4], data: fmt.Sprintf(`{"n":%d,"color":%q,"nested":{"k":%d}}`, i, colors[i%4], i%5)}
		m := nats.NewMsg(s.subject)
		m.Data = []byte(s.data)
		if i%7 != 0 {
			s.typ = "type." + s.subject[4:5]
			m.Header.Set("ce-type", s.typ)
		}
		m.Header.Set("x-shard", fmt.Sprint(i%2))
		ack, err := js.PublishMsg(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		s.seq = ack.Sequence
		all = append(all, s)
	}
	return js, all, mark
}

func expect(all []stored, keep func(stored) bool, backward bool) []uint64 {
	var out []uint64
	for _, s := range all {
		if keep(s) {
			out = append(out, s.seq)
		}
	}
	if backward {
		slices.Reverse(out)
	}
	return out
}

func pageAll(t *testing.T, e *Engine, r Request) ([]uint64, int) {
	t.Helper()
	var seqs []uint64
	pages := 0
	for {
		res, err := e.Run(context.Background(), r)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		for _, m := range res.Messages {
			seqs = append(seqs, m.Seq)
		}
		if res.Next == "" {
			return seqs, pages
		}
		r.Cursor = res.Next
		if pages > 1000 {
			t.Fatal("paging does not terminate")
		}
	}
}

func TestQueries(t *testing.T) {
	js, all, mark := fixture(t)
	e := New(js, 4, DefaultBudget)
	markSeq := all[400].seq
	cases := []struct {
		name string
		req  Request
		keep func(stored) bool
	}{
		{"forward all", Request{}, func(stored) bool { return true }},
		{"forward one subject", Request{Subjects: []string{"evt.b.y.updated"}}, func(s stored) bool { return s.subject == "evt.b.y.updated" }},
		{"forward wildcard", Request{Subjects: []string{"evt.*.x.>"}}, func(s stored) bool { return s.subject == "evt.a.x.created" }},
		{"forward two subjects (consumer)", Request{Subjects: []string{"evt.a.>", "evt.c.>"}}, func(s stored) bool { return s.subject != "evt.b.y.updated" }},
		{"forward from seq", Request{StartSeq: 590}, func(s stored) bool { return s.seq >= 590 }},
		{"forward from time", Request{StartTime: mark}, func(s stored) bool { return s.seq >= markSeq }},
		{"forward from time two subjects", Request{StartTime: mark, Subjects: []string{"evt.a.>", "evt.b.>"}},
			func(s stored) bool { return s.seq >= markSeq && s.subject != "evt.c.z.deleted" }},
		{"type", Request{Type: "type.a"}, func(s stored) bool { return s.typ == "type.a" }},
		{"untyped", Request{Type: UntypedType}, func(s stored) bool { return s.typ == "" }},
		{"header", Request{Headers: map[string]string{"x-shard": "1"}}, func(s stored) bool { return s.seq%2 == 0 }},
		{"contains", Request{Contains: `"color":"green"`}, func(s stored) bool { return s.color == "green" }},
		{"json path", Request{JSONPath: "nested.k", JSONValue: "3"}, func(s stored) bool { return int(s.seq-1)%5 == 3 }},
		{"combined", Request{Subjects: []string{"evt.a.>"}, Type: "type.a", JSONPath: "color", JSONValue: "red"},
			func(s stored) bool { return s.subject == "evt.a.x.created" && s.typ == "type.a" && s.color == "red" }},
	}
	for _, c := range cases {
		for _, dir := range []Direction{Forward, Backward} {
			if dir == Backward && !c.req.StartTime.IsZero() {
				continue
			}
			for _, limit := range []int{7, 50, 500} {
				t.Run(fmt.Sprintf("%s/%s/%d", c.name, dir, limit), func(t *testing.T) {
					r := c.req
					r.Stream, r.Dir, r.Limit = streams.Events, dir, limit
					if dir == Backward && r.StartSeq != 0 {
						r.StartSeq = 0 // backward start means "end at"; covered separately
					}
					keep := c.keep
					if dir == Backward && c.req.StartSeq != 0 {
						keep = func(stored) bool { return true }
					}
					got, _ := pageAll(t, e, r)
					want := expect(all, keep, dir == Backward)
					if !slices.Equal(got, want) {
						t.Fatalf("got %d seqs %v…, want %d %v…", len(got), head(got), len(want), head(want))
					}
				})
			}
		}
	}
}

func head(s []uint64) []uint64 { return s[:min(len(s), 8)] }

func TestBackwardEndsAt(t *testing.T) {
	js, _, _ := fixture(t)
	e := New(js, 4, DefaultBudget)
	res, err := e.Run(context.Background(), Request{Stream: streams.Events, Dir: Backward, StartSeq: 100, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := []uint64{res.Messages[0].Seq, res.Messages[1].Seq, res.Messages[2].Seq}; !slices.Equal(got, []uint64{100, 99, 98}) {
		t.Fatalf("%v", got)
	}
	if res.FirstSeq != 1 || res.LastSeq != 600 {
		t.Fatalf("bounds %d..%d", res.FirstSeq, res.LastSeq)
	}
}

func TestCursorTampering(t *testing.T) {
	js, _, _ := fixture(t)
	e := New(js, 4, DefaultBudget)
	ctx := context.Background()
	res, err := e.Run(ctx, Request{Stream: streams.Events, Subjects: []string{"evt.a.>"}, Limit: 5})
	if err != nil || res.Next == "" {
		t.Fatalf("%v", err)
	}
	for name, r := range map[string]Request{
		"other filter":    {Stream: streams.Events, Subjects: []string{"evt.b.>"}, Limit: 5, Cursor: res.Next},
		"other direction": {Stream: streams.Events, Subjects: []string{"evt.a.>"}, Dir: Backward, Limit: 5, Cursor: res.Next},
		"other type":      {Stream: streams.Events, Subjects: []string{"evt.a.>"}, Type: "x", Limit: 5, Cursor: res.Next},
		"garbage":         {Stream: streams.Events, Subjects: []string{"evt.a.>"}, Limit: 5, Cursor: "!!!"},
	} {
		if _, err := e.Run(ctx, r); !errors.Is(err, ErrCursor) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBudgetPartialAndResume(t *testing.T) {
	js, all, _ := fixture(t)
	e := New(js, 4, Budget{MaxMsgs: 100, MaxBytes: 64 << 20, MaxTime: 3 * time.Second})
	r := Request{Stream: streams.Events, Contains: "never-matches", Limit: 10}
	res, err := e.Run(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Partial || res.Reason != "msgs" || res.Next == "" || len(res.Messages) != 0 || res.Scanned != 100 {
		t.Fatalf("%+v", res)
	}
	got, pages := pageAll(t, e, r)
	if len(got) != 0 || pages < len(all)/100 {
		t.Fatalf("resume: %d msgs over %d pages", len(got), pages)
	}
	eb := New(js, 4, Budget{MaxMsgs: 50_000, MaxBytes: 2000, MaxTime: 3 * time.Second})
	res, err = eb.Run(context.Background(), r)
	if err != nil || !res.Partial || res.Reason != "bytes" {
		t.Fatalf("bytes budget: %+v %v", res, err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	js, _, _ := fixture(t)
	e := New(js, 1, DefaultBudget)
	e.sem <- struct{}{} // hold the only slot
	if _, err := e.Run(context.Background(), Request{Stream: streams.Events}); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	<-e.sem
}

func TestValidation(t *testing.T) {
	for name, r := range map[string]Request{
		"no stream":          {},
		"bad dir":            {Stream: "EVENTS", Dir: "sideways"},
		"time backward":      {Stream: "EVENTS", Dir: Backward, StartTime: time.Now()},
		"time and seq":       {Stream: "EVENTS", StartSeq: 3, StartTime: time.Now()},
		"path without value": {Stream: "EVENTS", JSONPath: "a"},
	} {
		if err := r.normalize(); !errors.Is(err, ErrRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSubjectMatches(t *testing.T) {
	for _, c := range []struct {
		f, s string
		ok   bool
	}{
		{"evt.>", "evt.a.b.c", true}, {"evt.>", "evt", false}, {"evt.*.b.c", "evt.a.b.c", true}, {"evt.*.b", "evt.a.b.c", false},
		{"evt.a.b.c", "evt.a.b.c", true}, {"evt.a.b.c", "evt.a.b.d", false}, {"evt.a.>", "evt.a", false},
	} {
		if SubjectMatches(c.f, c.s) != c.ok {
			t.Errorf("%s ~ %s", c.f, c.s)
		}
	}
}

func TestJSONLookup(t *testing.T) {
	data := []byte(`{"a":{"b":[1,{"c":"x"}],"t":true,"n":null}}`)
	for path, want := range map[string]string{"a.b.0": "1", "a.b.1.c": "x", "a.t": "true", "a.n": "null"} {
		if got, ok := jsonLookup(data, path); !ok || got != want {
			t.Errorf("%s = %q %v", path, got, ok)
		}
	}
	for _, p := range []string{"a.z", "a.b.9", "a.b.x", "x"} {
		if _, ok := jsonLookup(data, p); ok {
			t.Errorf("%s found", p)
		}
	}
	if _, ok := jsonLookup([]byte("not json"), "a"); ok {
		t.Error("non-JSON matched")
	}
}

func TestMessageSize(t *testing.T) {
	h := nats.Header{"ce-type": {"home.door.opened.v1"}, "x": {"a", "bc"}}
	// subject 20 + payload 16 + "ce-type" 7 + 19 + "x" 1 + 1 + 2
	if got := MessageSize("evt.home.door.opened", h, []byte(`{"door":"front"}`)); got != 66 {
		t.Errorf("size = %d, want 66", got)
	}
	if got := MessageSize("evt.a.b.c", nil, nil); got != 9 {
		t.Errorf("no headers or payload: size = %d, want 9", got)
	}
}
