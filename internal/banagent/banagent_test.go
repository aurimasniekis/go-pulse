package banagent

import (
	"context"
	"math/big"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/ban"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

func TestDiff(t *testing.T) {
	now := time.Now()
	want := []ban.Entry{
		{Network: "198.51.100.7/32", ExpiresAt: now.Add(time.Hour)},
		{Network: "203.0.113.0/24"},
		{Network: "2001:db8:1:2::/64", ExpiresAt: now.Add(2 * time.Hour)},
	}
	have := []Element{
		{Prefix: netip.MustParsePrefix("198.51.100.7/32"), ExpiresAt: now.Add(59 * time.Minute)},   // same ban, slightly older clock
		{Prefix: netip.MustParsePrefix("2001:db8:1:2::/64"), ExpiresAt: now.Add(10 * time.Minute)}, // extended: re-add
		{Prefix: netip.MustParsePrefix("192.0.2.1/32")},                                            // lifted: delete
	}
	add, del, err := Diff(want, have)
	if err != nil {
		t.Fatal(err)
	}
	keys := func(es []Element) []string {
		out := []string{}
		for _, e := range es {
			out = append(out, e.Prefix.String())
		}
		sort.Strings(out)
		return out
	}
	if got := keys(add); len(got) != 2 || got[0] != "2001:db8:1:2::/64" || got[1] != "203.0.113.0/24" {
		t.Fatalf("add %v", got)
	}
	if got := keys(del); len(got) != 2 || got[0] != "192.0.2.1/32" || got[1] != "2001:db8:1:2::/64" {
		t.Fatalf("del %v", got)
	}
	if _, _, err := Diff([]ban.Entry{{Network: "nope"}}, nil); err == nil {
		t.Fatal("bad network accepted")
	}
}

func TestIntervals(t *testing.T) {
	for _, c := range []struct {
		prefix string
		end    string
	}{
		{"198.51.100.7/32", "198.51.100.8"}, {"203.0.113.0/24", "203.0.114.0"}, {"2001:db8:1:2::/64", "2001:db8:1:3::"},
		{"255.255.255.255/32", "0.0.0.0"},
	} {
		p := netip.MustParsePrefix(c.prefix)
		e := end(p)
		got, _ := netip.AddrFromSlice(e)
		if got.String() != c.end {
			t.Errorf("end(%s) = %s want %s", p, got, c.end)
		}
		if c.end != "0.0.0.0" {
			if bits := prefixBits(p.Addr().AsSlice(), []*big.Int{new(big.Int).SetBytes(e), big.NewInt(1)}); bits != p.Bits() {
				t.Errorf("prefixBits(%s) = %d", p, bits)
			}
		}
	}
	if bits := prefixBits(netip.MustParseAddr("10.0.0.0").AsSlice(), []*big.Int{new(big.Int).SetBytes(netip.MustParseAddr("10.0.0.3").AsSlice())}); bits != 32 {
		t.Fatalf("non power-of-two interval: %d", bits)
	}
}

type fakeBackend struct {
	mu  sync.Mutex
	set map[netip.Prefix]time.Time
	ch  chan struct{}
}

func (f *fakeBackend) List() ([]Element, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Element{}
	for p, e := range f.set {
		out = append(out, Element{Prefix: p, ExpiresAt: e})
	}
	return out, nil
}

func (f *fakeBackend) Apply(add, del []Element, _ time.Time) error {
	f.mu.Lock()
	for _, e := range del {
		delete(f.set, e.Prefix)
	}
	for _, e := range add {
		f.set[e.Prefix] = e.ExpiresAt
	}
	f.mu.Unlock()
	f.ch <- struct{}{}
	return nil
}

func TestAgentFollowsFeed(t *testing.T) {
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, _ := ban.New(db, ban.Options{})
	_ = s.Load(context.Background())
	srv := httptest.NewServer(ban.NewFeed(s, "tok"))
	defer srv.Close()
	fb := &fakeBackend{set: map[netip.Prefix]time.Time{netip.MustParsePrefix("192.0.2.9/32"): {}}, ch: make(chan struct{}, 10)}
	a := &Agent{URL: srv.URL, Token: "tok", Backend: fb}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	<-fb.ch // initial sync removes the stale element
	for range 5 {
		s.Fail(context.Background(), "198.51.100.7", "nats", "")
	}
	select {
	case <-fb.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not pick up the new ban")
	}
	got, _ := fb.List()
	if len(got) != 1 || got[0].Prefix.String() != "198.51.100.7/32" || got[0].ExpiresAt.IsZero() {
		t.Fatalf("set %+v", got)
	}
}
