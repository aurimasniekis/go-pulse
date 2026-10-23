package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/auth"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/hooks"
	"github.com/aurimasniekis/go-pulse/internal/outbound"
	"github.com/aurimasniekis/go-pulse/internal/query"
	"github.com/aurimasniekis/go-pulse/internal/rollup"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/tail"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
	"github.com/aurimasniekis/go-pulse/internal/tokens"
	"github.com/aurimasniekis/go-pulse/pkg/pulse"
)

var authFailures atomic.Int64

type harness struct {
	admin  *httptest.Server
	public *httptest.Server
	app    *pulse.Client
	db     *store.DB
	deps   *Deps
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	env := natstest.Start(t)
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: env.Layout.DatabaseFile()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sys := sysapi.New(env.SysConn())
	if _, err := clients.New(db, env.Keys, sys, nil).Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	nc := env.AppConn()
	js, _ := jetstream.New(nc)
	if _, err := streams.EnsureEvents(ctx, js, 256<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	app, _ := pulse.New(nc, "api-test")
	ro := rollup.New(js, db, rollup.Options{Streams: []string{streams.Events}, FlushEvery: 50 * time.Millisecond})
	rctx, rcancel := context.WithCancel(context.Background())
	rdone := make(chan struct{})
	go func() { ro.Run(rctx); close(rdone) }()
	t.Cleanup(func() { rcancel(); <-rdone })
	d := &Deps{Version: "test", Profile: "dev", DB: db, NC: nc, JS: js, Sys: sys, Query: query.New(js, 4, query.DefaultBudget),
		Tail: tail.New(js, 2, 1000), Rollup: ro, Auth: auth.None{}, Ready: func() bool { return true },
		Tokens: tokens.New(db), Clients: clients.New(db, env.Keys, sys, nil), PublicURL: "https://events.example.test"}
	d.Hooks = hooks.New(db, js, env.MasterKey, nil)
	ob, err := outbound.New(db, js, env.MasterKey, outbound.Options{Allow: []string{"127.0.0.0/8"}, Backoff: []time.Duration{50 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	octx, ocancel := context.WithCancel(context.Background())
	if err := ob.Start(octx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ocancel(); ob.Wait() })
	d.Outbound = ob
	d.OnAuthFailure = func(ip, source string) { authFailures.Add(1) }
	h := &harness{admin: httptest.NewServer(NewAdmin(d)), public: httptest.NewServer(NewPublic(d)), app: app, db: db, deps: d}
	t.Cleanup(h.admin.Close)
	t.Cleanup(h.public.Close)
	return h
}

func (h *harness) publish(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		if _, err := h.app.Publish(context.Background(), "evt.test.thing.created", "test.thing.created.v1", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
}

func do(t *testing.T, method, url string, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, url, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

var csrfH = map[string]string{CSRFHeader: "1"}

func TestAdminGuards(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	resp, body := do(t, "GET", base+"/system/info", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"auth_mode":"none"`) || !strings.Contains(body, `"connected":true`) {
		t.Fatalf("info %d %s", resp.StatusCode, body)
	}
	for _, hk := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
		if resp.Header.Get(hk) == "" {
			t.Errorf("missing %s", hk)
		}
	}
	// DNS rebinding: a foreign Host is refused.
	if resp, _ := do(t, "GET", base+"/system/info", "", map[string]string{"Host": "evil.example:8080"}); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host: %d", resp.StatusCode)
	}
	// CSRF header required for writes.
	in := `{"name":"ORDERS","subjects":["orders.>"]}`
	if resp, _ := do(t, "POST", base+"/streams", in, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	if resp, body := do(t, "POST", base+"/streams", in, csrfH); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
}

func TestStreamsCRUD(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	for name, in := range map[string]string{
		"overlaps evt":      `{"name":"X1","subjects":["evt.custom.>"]}`,
		"overlaps wildcard": `{"name":"X2","subjects":[">"]}`,
		"overlaps star":     `{"name":"X3","subjects":["*.a.b"]}`,
		"reserved name":     `{"name":"EVENTS","subjects":["x.>"]}`,
		"kv name":           `{"name":"KV_x","subjects":["x.>"]}`,
		"bad subject":       `{"name":"X4","subjects":["a..b"]}`,
	} {
		if resp, body := do(t, "POST", base+"/streams", in, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, body := do(t, "POST", base+"/streams", `{"name":"ORDERS","subjects":["orders.>"],"max_age_seconds":3600}`, csrfH); resp.StatusCode != 201 {
		t.Fatalf("create %d %s", resp.StatusCode, body)
	}
	_, body := do(t, "GET", base+"/streams", "", nil)
	var list []StreamDTO
	_ = json.Unmarshal([]byte(body), &list)
	names := map[string]StreamDTO{}
	for _, s := range list {
		names[s.Name] = s
	}
	if !names["EVENTS"].Managed || names["ORDERS"].Managed || names["ORDERS"].MaxAgeSeconds != 3600 {
		t.Fatalf("list %+v", names)
	}
	if resp, body := do(t, "PUT", base+"/streams/ORDERS", `{"name":"ORDERS","subjects":["orders.>","returns.>"]}`, csrfH); resp.StatusCode != 200 {
		t.Fatalf("update %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "DELETE", base+"/streams/EVENTS?confirm=EVENTS", "", csrfH); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete EVENTS: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "DELETE", base+"/streams/ORDERS?confirm=nope", "", csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("delete without confirm: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "DELETE", base+"/streams/ORDERS?confirm=ORDERS", "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/streams/ORDERS", "", nil); resp.StatusCode != 404 {
		t.Fatalf("get deleted: %d", resp.StatusCode)
	}
	var n int
	_ = h.db.R().QueryRow("SELECT COUNT(*) FROM audit WHERE action LIKE 'stream.%' AND result = 'ok'").Scan(&n)
	if n != 3 {
		t.Fatalf("audit rows %d", n)
	}
	// Consumers of EVENTS: a durable shows up and can be deleted with confirmation.
	if _, err := h.deps.JS.CreateOrUpdateConsumer(context.Background(), "EVENTS", jetstream.ConsumerConfig{Durable: "probe"}); err != nil {
		t.Fatal(err)
	}
	_, body = do(t, "GET", base+"/streams/EVENTS/consumers", "", nil)
	if !strings.Contains(body, `"name":"probe"`) {
		t.Fatalf("consumers: %s", body)
	}
	if resp, _ := do(t, "DELETE", base+"/streams/EVENTS/consumers/probe?confirm=probe", "", csrfH); resp.StatusCode != 204 {
		t.Fatalf("delete consumer: %d", resp.StatusCode)
	}
}

func TestEventsEndpoints(t *testing.T) {
	h := newHarness(t)
	h.publish(t, 30)
	base := h.admin.URL + AdminPrefix
	resp, body := do(t, "GET", base+"/events?limit=10", "", nil)
	var page EventsPage
	if err := json.Unmarshal([]byte(body), &page); err != nil || resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if len(page.Events) != 10 || page.Events[0].Seq != 30 || page.Next == "" || page.LastSeq != 30 {
		t.Fatalf("page %+v", page)
	}
	e := page.Events[0]
	if e.Type != "test.thing.created.v1" || e.Source != "api-test" || e.Encoding != "utf8" || !e.SubjectValid || e.Untyped {
		t.Fatalf("dto %+v", e)
	}
	_, body = do(t, "GET", base+"/events?limit=10&cursor="+page.Next, "", nil)
	var p2 EventsPage
	_ = json.Unmarshal([]byte(body), &p2)
	if p2.Events[0].Seq != 20 {
		t.Fatalf("page 2 starts at %d", p2.Events[0].Seq)
	}
	if resp, _ := do(t, "GET", base+"/events?limit=10&type=x&cursor="+page.Next, "", nil); resp.StatusCode != 400 {
		t.Fatalf("mismatched cursor: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/events?header=novalue", "", nil); resp.StatusCode != 400 {
		t.Fatalf("bad header filter: %d", resp.StatusCode)
	}
	if resp, body := do(t, "GET", base+"/events/EVENTS/5", "", nil); resp.StatusCode != 200 || !strings.Contains(body, `"seq":5`) {
		t.Fatalf("get one: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "GET", base+"/events/EVENTS/999", "", nil); resp.StatusCode != 404 {
		t.Fatalf("missing: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", base+"/events?stream=NOPE", "", nil); resp.StatusCode != 404 {
		t.Fatalf("missing stream: %d", resp.StatusCode)
	}
}

type sseEvent struct{ id, event, data string }

func readSSE(t *testing.T, r *bufio.Reader, n int) []sseEvent {
	t.Helper()
	var out []sseEvent
	var cur sseEvent
	for len(out) < n {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("sse read: %v (got %d)", err, len(out))
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if cur.data != "" {
				out = append(out, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			cur.data += line[6:]
		}
	}
	return out
}

func TestTailSSE(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	open := func(hdr map[string]string) (*http.Response, *bufio.Reader) {
		req, _ := http.NewRequestWithContext(ctx, "GET", h.admin.URL+AdminPrefix+"/tail?subject=evt.test.>", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp, bufio.NewReader(resp.Body)
	}
	resp, r := open(nil)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	time.Sleep(200 * time.Millisecond) // DeliverNew: subscribe before publishing
	h.publish(t, 3)
	evs := readSSE(t, r, 3)
	if evs[0].event != "event" || evs[0].id != "1" || evs[2].id != "3" || !strings.Contains(evs[0].data, `"subject":"evt.test.thing.created"`) {
		t.Fatalf("%+v", evs)
	}
	// Resume after id 2 replays 3.
	_, r2 := open(map[string]string{"Last-Event-ID": "2"})
	if got := readSSE(t, r2, 1); got[0].id != "3" {
		t.Fatalf("resume: %+v", got)
	}
	// Max sessions is 2 in this harness: the third gets an error notice.
	_, r3 := open(nil)
	if got := readSSE(t, r3, 1); got[0].event != "notice" || !strings.Contains(got[0].data, "too many") {
		t.Fatalf("third session: %+v", got)
	}
}

func TestUIAndPublic(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/", "/clients/abc"} {
		resp, body := do(t, "GET", h.admin.URL+p, "", nil)
		if resp.StatusCode != 200 || !strings.Contains(body, "<html") && !strings.Contains(body, "<!doctype html") {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	if resp, _ := do(t, "GET", h.admin.URL+AdminPrefix+"/nope", "", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown admin api path: %d", resp.StatusCode)
	}
	pub := h.public.URL
	for p, want := range map[string]int{
		"/healthz": 200, PublicPrefix + "/health": 200, "/": 404, "/clients": 404,
		AdminPrefix + "/system/info": 404, "/metrics": 404,
	} {
		if resp, _ := do(t, "GET", pub+p, "", nil); resp.StatusCode != want {
			t.Errorf("public %s: %d want %d", p, resp.StatusCode, want)
		}
	}
	limited := 0
	for range 60 {
		if resp, _ := do(t, "GET", pub+"/healthz", "", nil); resp.StatusCode == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("per-IP rate limit never triggered")
	}
}

func TestSubjectsCollide(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"evt.>", "evt.a.b", true}, {"evt.>", "orders.>", false}, {">", "evt.>", true}, {"*.a", "evt.a", true},
		{"*.a", "evt.b", false}, {"evt", "evt.>", false}, {"evt.*", "evt.>", true}, {"a.b.c", "a.b", false},
		{"a.*.c", "a.b.*", true}, {"_sys.alert.x", "_sys.>", true},
	} {
		if got := SubjectsCollide(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s = %v", c.a, c.b, got)
		}
		if got := SubjectsCollide(c.b, c.a); got != c.want {
			t.Errorf("%s vs %s = %v (symmetric)", c.b, c.a, got)
		}
	}
	_ = fmt.Sprint()
}

func TestRollupEndpoints(t *testing.T) {
	h := newHarness(t)
	h.publish(t, 12)
	base := h.admin.URL + AdminPrefix
	var ov Overview
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := do(t, "GET", base+"/overview", "", nil)
		_ = json.Unmarshal([]byte(body), &ov)
		if ov.Last1h.Msgs == 12 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ov.Msgs != 12 || ov.Last1h.Msgs != 12 || ov.Last24h.Msgs != 12 || len(ov.TopTypes24h) != 1 || ov.Index.Indexed != 1 ||
		ov.Forecast.MaxBytes != 256<<20 || len(ov.TopSubjects7d) != 1 || ov.TopSubjects7d[0].Key != "evt.test.thing.created" {
		t.Fatalf("overview %+v", ov)
	}
	for _, q := range []string{"range=1h", "range=24h&dim=type", "range=1h&dim=p3", "range=1y", "range=30d&dim=p4&top=3"} {
		resp, body := do(t, "GET", base+"/throughput?"+q, "", nil)
		var ss SeriesSet
		_ = json.Unmarshal([]byte(body), &ss)
		if resp.StatusCode != 200 || len(ss.Series) != 1 || len(ss.Series[0].Points) == 0 {
			t.Fatalf("%s: %d %s", q, resp.StatusCode, body)
		}
		var sum int64
		for _, p := range ss.Series[0].Points {
			sum += p.Msgs
		}
		if sum != 12 {
			t.Fatalf("%s: sum %d", q, sum)
		}
	}
	if resp, _ := do(t, "GET", base+"/throughput?range=2h", "", nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad range: %d", resp.StatusCode)
	}
	_, body := do(t, "GET", base+"/subjects/tree?prefix=evt.test", "", nil)
	if !strings.Contains(body, `"token":"thing"`) || !strings.Contains(body, `"msgs":12`) {
		t.Fatalf("tree %s", body)
	}
	if resp, _ := do(t, "GET", base+"/subjects/info?subject=evt.nope.x.y", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing subject: %d", resp.StatusCode)
	}
	if resp, body := do(t, "GET", base+"/subjects/info?subject=evt.test.thing.created", "", nil); resp.StatusCode != 200 || !strings.Contains(body, `"last_type":"test.thing.created.v1"`) {
		t.Fatalf("subject: %d %s", resp.StatusCode, body)
	}
}

func TestAdminPublish(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	body := `{"subject":"evt.home.door.opened","type":"home.door.opened.v1","data":"{\"door\":\"front\"}","content_type":"application/json","headers":{"x-test":"1"},"id":"fixed-1"}`
	resp, out := do(t, "POST", base+"/events", body, csrfH)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(out, `"seq":1`) {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	if _, out := do(t, "POST", base+"/events", body, csrfH); !strings.Contains(out, `"duplicate":true`) {
		t.Fatalf("dedupe: %s", out)
	}
	if resp, _ := do(t, "POST", base+"/events", `{"subject":"evt.bad"}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad subject: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", base+"/events", body, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	_, out = do(t, "GET", base+"/events/EVENTS/1", "", nil)
	if !strings.Contains(out, `"source":"dashboard"`) || !strings.Contains(out, `"x-test":["1"]`) {
		t.Fatalf("stored: %s", out)
	}
}
