package ban

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newSvc(t *testing.T, o Options) (*Service, *clock, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	o.Now = c.now
	o.IsCloudflare = func(a netip.Addr) bool { return netip.MustParsePrefix("104.16.0.0/13").Contains(a) }
	s, err := New(db, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Store timestamps use the real clock elsewhere; bans use the service clock.
	return s, c, db
}

func fail(s *Service, ip string, n int) {
	for range n {
		s.Fail(context.Background(), ip, "nats", "test")
	}
}

func TestThresholdWindowAndGrouping(t *testing.T) {
	s, c, _ := newSvc(t, Options{})
	fail(s, "198.51.100.7", 4)
	if s.IsBanned("198.51.100.7") {
		t.Fatal("banned below threshold")
	}
	c.add(11 * time.Minute) // the first four fall out of the window
	fail(s, "198.51.100.7", 4)
	if s.IsBanned("198.51.100.7") {
		t.Fatal("old failures counted")
	}
	fail(s, "198.51.100.7", 1)
	if !s.IsBanned("198.51.100.7") || s.IsBanned("198.51.100.8") {
		t.Fatal("threshold ban")
	}
	// IPv6 addresses in one /64 count together and the ban covers the whole /64.
	for i := range 5 {
		s.Fail(context.Background(), "2001:db8:1:2::"+string(rune('a'+i)), "http_token", "")
	}
	if !s.IsBanned("2001:db8:1:2::ffff") || s.IsBanned("2001:db8:1:3::1") {
		t.Fatal("ipv6 /64 grouping")
	}
	bans, _ := s.Active(context.Background())
	if len(bans) != 2 || bans[0].ExpiresAt.Sub(bans[0].CreatedAt) != time.Hour {
		t.Fatalf("active %+v", bans)
	}
}

func TestEscalationAndCap(t *testing.T) {
	s, c, _ := newSvc(t, Options{})
	want := []time.Duration{time.Hour, 4 * time.Hour, 16 * time.Hour, 64 * time.Hour, 256 * time.Hour, 30 * 24 * time.Hour, 30 * 24 * time.Hour}
	for i, d := range want {
		fail(s, "203.0.113.9", 5)
		bans, _ := s.Active(context.Background())
		if len(bans) != 1 || bans[0].ExpiresAt.Sub(bans[0].CreatedAt) != d || bans[0].Offences != i+1 {
			t.Fatalf("offence %d: %+v", i+1, bans)
		}
		c.add(d + time.Second)
		_ = s.Prune(context.Background())
		if s.IsBanned("203.0.113.9") {
			t.Fatal("ban outlived its expiry")
		}
	}
	// After the offence memory passes, escalation starts over.
	c.add(31 * 24 * time.Hour)
	fail(s, "203.0.113.9", 5)
	bans, _ := s.Active(context.Background())
	if bans[0].ExpiresAt.Sub(bans[0].CreatedAt) != time.Hour {
		t.Fatalf("escalation not reset: %+v", bans[0])
	}
}

func TestNeverBan(t *testing.T) {
	s, _, _ := newSvc(t, Options{NeverBan: []string{"198.51.100.0/24"}})
	ctx := context.Background()
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.17.0.2", "192.168.1.5", "::1", "fd00::5", "104.16.1.1", "198.51.100.50"} {
		fail(s, ip, 10)
		if s.IsBanned(ip) {
			t.Errorf("%s banned", ip)
		}
		if _, err := s.Manual(ctx, ip, time.Hour, "x", audit.System); !errors.Is(err, ErrNeverBan) {
			t.Errorf("manual ban of %s: %v", ip, err)
		}
	}
	if r := s.Recent(); len(r) == 0 || r[0].Ignored == "" {
		t.Fatalf("ignored failures not reported: %+v", r[:1])
	}
}

func TestManualAllowlistAndPersistence(t *testing.T) {
	s, _, db := newSvc(t, Options{})
	ctx := context.Background()
	b, err := s.Manual(ctx, "203.0.113.0/24", 0, "scanner", audit.Actor{Name: "admin"})
	if err != nil || !b.ExpiresAt.IsZero() || !s.IsBanned("203.0.113.77") {
		t.Fatalf("manual: %+v %v", b, err)
	}
	// A fresh service (restart) loads bans from the database.
	s2, err := New(db, Options{Now: s.o.Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Load(ctx); err != nil || !s2.IsBanned("203.0.113.1") {
		t.Fatalf("not persisted: %v", err)
	}
	if err := s.Allow(ctx, "203.0.113.64/26", "office", audit.Actor{Name: "admin"}); err != nil {
		t.Fatal(err)
	}
	if s.IsBanned("203.0.113.70") {
		t.Fatal("allowlisting did not lift the overlapping ban")
	}
	fail(s, "203.0.113.70", 10)
	if s.IsBanned("203.0.113.70") {
		t.Fatal("allowlisted address banned")
	}
	if err := s.Unban(ctx, "203.0.113.0/24", audit.System); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unban of lifted ban: %v", err)
	}
	if err := s.Disallow(ctx, "203.0.113.64/26", audit.System); err != nil {
		t.Fatal(err)
	}
	fail(s, "203.0.113.70", 5)
	if !s.IsBanned("203.0.113.70") {
		t.Fatal("ban after allowlist removal")
	}
	var n int
	_ = db.R().QueryRow("SELECT COUNT(*) FROM audit WHERE action LIKE 'ban.%'").Scan(&n)
	if n < 5 {
		t.Fatalf("ban actions audited: %d", n)
	}
}

func TestMiddlewareAndFeed(t *testing.T) {
	s, _, _ := newSvc(t, Options{})
	h := s.Middleware(func(r *http.Request) string { return r.Header.Get("X-Test-IP") }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	fail(s, "198.51.100.9", 5)
	for ip, want := range map[string]int{"198.51.100.9": 403, "198.51.100.10": 200} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Test-IP", ip)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d", ip, w.Code)
		}
	}
	feed := NewFeed(s, "tok-123")
	srv := httptest.NewServer(feed)
	defer srv.Close()
	get := func(q, tok string) (int, BansResponse) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+q, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var br BansResponse
		_ = json.NewDecoder(resp.Body).Decode(&br)
		return resp.StatusCode, br
	}
	if code, _ := get("", "nope"); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	code, br := get("", "tok-123")
	if code != 200 || len(br.Bans) != 1 || br.Bans[0].Network != "198.51.100.9/32" {
		t.Fatalf("feed %d %+v", code, br)
	}
	done := make(chan BansResponse)
	go func() {
		_, r := get("?wait="+uitoa(br.Generation), "tok-123")
		done <- r
	}()
	time.Sleep(100 * time.Millisecond)
	fail(s, "198.51.100.11", 5)
	select {
	case r := <-done:
		if len(r.Bans) != 2 || r.Generation == br.Generation {
			t.Fatalf("long poll %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long poll did not wake on a new ban")
	}
}

func TestCloudflareReconcile(t *testing.T) {
	var mu sync.Mutex
	items := []cfItem{{ID: "keep", IP: "192.0.2.1", Comment: "added by hand"}, {ID: "stale", IP: "198.51.100.200", Comment: "pulse: banned until x"}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer cf-token" || !strings.HasPrefix(r.URL.Path, "/accounts/acc/rules/lists/lst/items") {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"message":"denied"}]}`)
			return
		}
		switch r.Method {
		case http.MethodGet:
			b, _ := json.Marshal(items)
			_, _ = io.WriteString(w, `{"success":true,"result":`+string(b)+`,"result_info":{"cursors":{}}}`)
		case http.MethodPost:
			var add []cfItem
			_ = json.NewDecoder(r.Body).Decode(&add)
			for i, a := range add {
				a.ID = "new" + uitoa(uint64(i))
				items = append(items, a)
			}
			_, _ = io.WriteString(w, `{"success":true,"result":{"operation_id":"op"}}`)
		case http.MethodDelete:
			var del struct{ Items []struct{ ID string } }
			_ = json.NewDecoder(r.Body).Decode(&del)
			kept := items[:0]
			for _, it := range items {
				gone := false
				for _, d := range del.Items {
					gone = gone || d.ID == it.ID
				}
				if !gone {
					kept = append(kept, it)
				}
			}
			items = kept
			_, _ = io.WriteString(w, `{"success":true,"result":{"operation_id":"op"}}`)
		}
	}))
	defer api.Close()
	c := &CloudflareList{BaseURL: api.URL, AccountID: "acc", ListID: "lst", Token: "cf-token", HTTP: api.Client()}
	now := time.Now()
	bans := []Entry{
		{Network: "203.0.113.5/32", ExpiresAt: now.Add(4 * time.Hour)},
		{Network: "2001:db8:1:2::/64"},                                   // permanent
		{Network: "203.0.113.6/32", ExpiresAt: now.Add(5 * time.Minute)}, // short: not mirrored
	}
	added, removed, err := c.Reconcile(context.Background(), bans, now)
	if err != nil || added != 2 || removed != 1 {
		t.Fatalf("reconcile %d %d %v", added, removed, err)
	}
	ips := map[string]bool{}
	for _, it := range items {
		ips[it.IP] = true
	}
	if !ips["192.0.2.1"] || !ips["203.0.113.5"] || !ips["2001:db8:1:2::/64"] || ips["198.51.100.200"] || ips["203.0.113.6"] {
		t.Fatalf("list after sync %v", ips)
	}
	if a, r, err := c.Reconcile(context.Background(), bans, now); err != nil || a+r != 0 {
		t.Fatalf("second reconcile not idempotent: %d %d %v", a, r, err)
	}
	bad := *c
	bad.Token = "wrong"
	if _, _, err := bad.Reconcile(context.Background(), bans, now); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("api error: %v", err)
	}
}
