//go:build linux

package banagent

import (
	"fmt"
	"math/big"
	"net/netip"
	"time"

	"github.com/google/nftables"
)

// NFT manipulates the ban sets of an existing inet table with netlink. The table and sets are created by Ansible:
//
//	table inet pulse_edge {
//	  set pulse_ban_v4 { type ipv4_addr; flags interval, timeout; }
//	  set pulse_ban_v6 { type ipv6_addr; flags interval, timeout; }
//	  …
//	}
type NFT struct {
	Table string // e.g. pulse_edge
	SetV4 string
	SetV6 string
}

func (n *NFT) sets(c *nftables.Conn) (*nftables.Set, *nftables.Set, error) {
	t := &nftables.Table{Name: n.Table, Family: nftables.TableFamilyINet}
	v4, err := c.GetSetByName(t, n.SetV4)
	if err != nil {
		return nil, nil, fmt.Errorf("nft set %s/%s: %w (is the firewall role applied?)", n.Table, n.SetV4, err)
	}
	v6, err := c.GetSetByName(t, n.SetV6)
	if err != nil {
		return nil, nil, fmt.Errorf("nft set %s/%s: %w", n.Table, n.SetV6, err)
	}
	return v4, v6, nil
}

// List reads both sets. Interval sets hold start elements followed by IntervalEnd markers.
func (n *NFT) List() ([]Element, error) {
	c, err := nftables.New()
	if err != nil {
		return nil, err
	}
	v4, v6, err := n.sets(c)
	if err != nil {
		return nil, err
	}
	var out []Element
	for _, s := range []*nftables.Set{v4, v6} {
		els, err := c.GetSetElements(s)
		if err != nil {
			return nil, err
		}
		var starts []nftables.SetElement
		var ends []*big.Int
		for _, e := range els {
			if _, ok := netip.AddrFromSlice(e.Key); !ok {
				continue
			}
			if e.IntervalEnd {
				ends = append(ends, new(big.Int).SetBytes(e.Key))
			} else {
				starts = append(starts, e)
			}
		}
		for _, e := range starts {
			a, _ := netip.AddrFromSlice(e.Key)
			el := Element{Prefix: netip.PrefixFrom(a.Unmap(), prefixBits(e.Key, ends))}
			if e.Expires > 0 {
				el.ExpiresAt = time.Now().Add(e.Expires)
			}
			out = append(out, el)
		}
	}
	return out, nil
}

func elements(p netip.Prefix, exp time.Time, now time.Time) []nftables.SetElement {
	start := nftables.SetElement{Key: p.Addr().AsSlice(), Comment: "pulse"}
	if !exp.IsZero() {
		start.Timeout = max(exp.Sub(now).Truncate(time.Second), time.Second)
	}
	return []nftables.SetElement{start, {Key: end(p), IntervalEnd: true}}
}

// Apply adds and deletes in one netlink batch.
func (n *NFT) Apply(add, del []Element, now time.Time) error {
	c, err := nftables.New()
	if err != nil {
		return err
	}
	v4, v6, err := n.sets(c)
	if err != nil {
		return err
	}
	pick := func(p netip.Prefix) *nftables.Set {
		if p.Addr().Is4() {
			return v4
		}
		return v6
	}
	for _, e := range del {
		if err := c.SetDeleteElements(pick(e.Prefix), elements(e.Prefix, time.Time{}, now)); err != nil {
			return err
		}
	}
	for _, e := range add {
		if err := c.SetAddElements(pick(e.Prefix), elements(e.Prefix, e.ExpiresAt, now)); err != nil {
			return err
		}
	}
	return c.Flush()
}
