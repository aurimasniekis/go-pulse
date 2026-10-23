//go:build linux && nft

package banagent

import (
	"net/netip"
	"sort"
	"testing"
	"time"

	"github.com/google/nftables"

	"github.com/aurimasniekis/go-pulse/internal/ban"
)

// Runs in a privileged Linux container (make test-nft): creates the same table layout the Ansible firewall role does,
// in the container's own network namespace.
func TestNFTReconcile(t *testing.T) {
	c, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := c.AddTable(&nftables.Table{Name: "pulse_test", Family: nftables.TableFamilyINet})
	for _, s := range []*nftables.Set{
		{Table: table, Name: "pulse_ban_v4", KeyType: nftables.TypeIPAddr, Interval: true, HasTimeout: true},
		{Table: table, Name: "pulse_ban_v6", KeyType: nftables.TypeIP6Addr, Interval: true, HasTimeout: true},
	} {
		if err := c.AddSet(s, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		c.DelTable(table)
		_ = c.Flush()
	})

	n := &NFT{Table: "pulse_test", SetV4: "pulse_ban_v4", SetV6: "pulse_ban_v6"}
	now := time.Now()
	want := []ban.Entry{
		{Network: "198.51.100.7/32", ExpiresAt: now.Add(time.Hour)},
		{Network: "203.0.113.0/24"},
		{Network: "2001:db8:1:2::/64", ExpiresAt: now.Add(2 * time.Hour)},
	}
	a := &Agent{Backend: n, Now: time.Now}
	if added, removed, err := a.Reconcile(want); err != nil || added != 3 || removed != 0 {
		t.Fatalf("first reconcile: %d %d %v", added, removed, err)
	}
	have, err := n.List()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range have {
		got = append(got, e.Prefix.String())
		if e.Prefix.String() == "198.51.100.7/32" && (e.ExpiresAt.IsZero() || e.ExpiresAt.Sub(now) < 59*time.Minute) {
			t.Errorf("timeout not set: %+v", e)
		}
		if e.Prefix.String() == "203.0.113.0/24" && !e.ExpiresAt.IsZero() {
			t.Errorf("permanent ban has a timeout: %+v", e)
		}
	}
	sort.Strings(got)
	if len(got) != 3 || got[0] != "198.51.100.7/32" || got[1] != "2001:db8:1:2::/64" || got[2] != "203.0.113.0/24" {
		t.Fatalf("sets hold %v", got)
	}
	if added, removed, err := a.Reconcile(want); err != nil || added+removed != 0 {
		t.Fatalf("second reconcile not idempotent: %d %d %v", added, removed, err)
	}
	if _, removed, err := a.Reconcile(want[:1]); err != nil || removed != 2 {
		t.Fatalf("lift: removed %d %v", removed, err)
	}
	have, _ = n.List()
	if len(have) != 1 || have[0].Prefix != netip.MustParsePrefix("198.51.100.7/32") {
		t.Fatalf("after lift %+v", have)
	}
}
