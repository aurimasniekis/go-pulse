// Package banagent mirrors pulsed's bans into nftables sets (PL-BAN-007). It runs as `pulsed banagent` on the host
// network with NET_ADMIN and touches nothing but the two ban sets in the Ansible-owned table.
package banagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/ban"
)

// Element is one banned range with an optional expiry.
type Element struct {
	Prefix    netip.Prefix
	ExpiresAt time.Time // zero: no timeout
}

// Backend reads and changes the two ban sets.
type Backend interface {
	// List returns the elements currently in the v4 and v6 sets.
	List() ([]Element, error)
	// Apply adds and removes elements atomically (one netlink batch).
	Apply(add, del []Element, now time.Time) error
}

// Diff computes what to add and delete so the sets match want. Elements are matched by prefix; a changed expiry on an
// existing element is left to the element's own timeout (re-adding would reset counters for no benefit), except that a
// ban made permanent, or extended by more than a minute, is re-added.
func Diff(want []ban.Entry, have []Element) (add, del []Element, err error) {
	w := map[netip.Prefix]time.Time{}
	for _, e := range want {
		p, perr := netip.ParsePrefix(e.Network)
		if perr != nil {
			return nil, nil, fmt.Errorf("banagent: bad network %q", e.Network)
		}
		w[p.Masked()] = e.ExpiresAt
	}
	h := map[netip.Prefix]time.Time{}
	for _, e := range have {
		h[e.Prefix] = e.ExpiresAt
	}
	for p, exp := range w {
		cur, ok := h[p]
		switch {
		case !ok:
			add = append(add, Element{Prefix: p, ExpiresAt: exp})
		case (exp.IsZero() && !cur.IsZero()) || (!exp.IsZero() && !cur.IsZero() && exp.Sub(cur) > time.Minute):
			del = append(del, Element{Prefix: p, ExpiresAt: cur})
			add = append(add, Element{Prefix: p, ExpiresAt: exp})
		}
	}
	for p, exp := range h {
		if _, ok := w[p]; !ok {
			del = append(del, Element{Prefix: p, ExpiresAt: exp})
		}
	}
	return add, del, nil
}

// Agent polls the feed and reconciles.
type Agent struct {
	URL     string
	Token   string
	Backend Backend
	HTTP    *http.Client
	Log     *slog.Logger
	Now     func() time.Time
}

func (a *Agent) fetch(ctx context.Context, wait uint64) (*ban.BansResponse, error) {
	u := a.URL
	if wait > 0 {
		u += "?wait=" + strconv.FormatUint(wait, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ban feed: %s", resp.Status)
	}
	var br ban.BansResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 16<<20)).Decode(&br); err != nil {
		return nil, err
	}
	return &br, nil
}

// Reconcile applies one snapshot.
func (a *Agent) Reconcile(bans []ban.Entry) (int, int, error) {
	have, err := a.Backend.List()
	if err != nil {
		return 0, 0, err
	}
	add, del, err := Diff(bans, have)
	if err != nil || len(add)+len(del) == 0 {
		return 0, 0, err
	}
	return len(add), len(del), a.Backend.Apply(add, del, a.Now())
}

// Run long-polls the feed until ctx ends. When pulsed is unreachable the sets are left as they are; element timeouts
// still expire bans.
func (a *Agent) Run(ctx context.Context) error {
	if a.Log == nil {
		a.Log = slog.Default()
	}
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.HTTP == nil {
		a.HTTP = &http.Client{Timeout: 40 * time.Second}
	}
	var gen uint64
	backoff := time.Second
	lastFull := time.Time{}
	for ctx.Err() == nil {
		wait := gen
		if time.Since(lastFull) > 5*time.Minute {
			wait = 0 // periodic full read, also repairs manual changes to the sets
		}
		br, err := a.fetch(ctx, wait)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			a.Log.Warn("ban feed unavailable; keeping current sets", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if wait == 0 {
			lastFull = time.Now()
		}
		gen = br.Generation
		added, removed, err := a.Reconcile(br.Bans)
		if err != nil {
			a.Log.Error("reconcile nftables", "err", err)
			continue
		}
		if added+removed > 0 {
			a.Log.Info("nftables ban sets updated", "added", added, "removed", removed, "total", len(br.Bans))
		}
	}
	return nil
}
