package ban

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// CloudflareList mirrors long bans into a Cloudflare account IP list (PL-BAN-008). Only items whose comment starts
// with commentPrefix are managed; anything else in the list is left alone.
type CloudflareList struct {
	BaseURL   string // https://api.cloudflare.com/client/v4
	AccountID string
	ListID    string
	Token     string
	MinBan    time.Duration // only bans at least this long are mirrored (default 1 h)
	HTTP      *http.Client
	Log       *slog.Logger
}

const commentPrefix = "pulse:"

type cfItem struct {
	ID      string `json:"id,omitempty"`
	IP      string `json:"ip"`
	Comment string `json:"comment,omitempty"`
}

func (c *CloudflareList) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Success bool                       `json:"success"`
		Errors  []struct{ Message string } `json:"errors"`
		Result  json.RawMessage            `json:"result"`
		Info    struct {
			Cursors struct {
				After string `json:"after"`
			} `json:"cursors"`
		} `json:"result_info"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&env); err != nil {
		return fmt.Errorf("cloudflare %s %s: %s: %w", method, path, resp.Status, err)
	}
	if !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("cloudflare %s %s: %s %s", method, path, resp.Status, strings.Join(msgs, "; "))
	}
	if out != nil {
		if p, ok := out.(*page); ok {
			p.after = env.Info.Cursors.After
			return json.Unmarshal(env.Result, &p.items)
		}
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

type page struct {
	items []cfItem
	after string
}

func (c *CloudflareList) items(ctx context.Context) ([]cfItem, error) {
	var all []cfItem
	cursor := ""
	for range 100 {
		path := fmt.Sprintf("/accounts/%s/rules/lists/%s/items?per_page=500", url.PathEscape(c.AccountID), url.PathEscape(c.ListID))
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var p page
		if err := c.do(ctx, http.MethodGet, path, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.items...)
		if p.after == "" {
			return all, nil
		}
		cursor = p.after
	}
	return nil, errors.New("cloudflare list: too many pages")
}

// cfIP formats a ban network the way Cloudflare lists expect: bare IPv4 for /32, CIDR otherwise.
func cfIP(network string) string {
	p, err := netip.ParsePrefix(network)
	if err == nil && p.Addr().Is4() && p.Bits() == 32 {
		return p.Addr().String()
	}
	return network
}

// Reconcile makes the managed list items equal to the long active bans.
func (c *CloudflareList) Reconcile(ctx context.Context, bans []Entry, now time.Time) (added, removed int, err error) {
	minBan := c.MinBan
	if minBan <= 0 {
		minBan = time.Hour
	}
	want := map[string]Entry{}
	for _, b := range bans {
		if b.ExpiresAt.IsZero() || b.ExpiresAt.Sub(now) >= minBan/2 { // long bans; keep mirrored until close to expiry
			want[cfIP(b.Network)] = b
		}
	}
	cur, err := c.items(ctx)
	if err != nil {
		return 0, 0, err
	}
	have := map[string]bool{}
	var del []map[string]string
	for _, it := range cur {
		if !strings.HasPrefix(it.Comment, commentPrefix) {
			continue
		}
		if _, ok := want[it.IP]; ok {
			have[it.IP] = true
			continue
		}
		del = append(del, map[string]string{"id": it.ID})
	}
	var add []cfItem
	for ip, b := range want {
		if !have[ip] {
			exp := "no expiry"
			if !b.ExpiresAt.IsZero() {
				exp = "until " + b.ExpiresAt.UTC().Format(time.RFC3339)
			}
			add = append(add, cfItem{IP: ip, Comment: commentPrefix + " banned " + exp})
		}
	}
	base := fmt.Sprintf("/accounts/%s/rules/lists/%s/items", url.PathEscape(c.AccountID), url.PathEscape(c.ListID))
	if len(add) > 0 {
		if err := c.do(ctx, http.MethodPost, base, add, nil); err != nil {
			return 0, 0, err
		}
	}
	if len(del) > 0 {
		if err := c.do(ctx, http.MethodDelete, base, map[string]any{"items": del}, nil); err != nil {
			return len(add), 0, err
		}
	}
	return len(add), len(del), nil
}

// Run reconciles on every ban change and at least every 5 minutes until ctx ends.
func (c *CloudflareList) Run(ctx context.Context, s *Service) {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	kick := make(chan struct{}, 1)
	s.OnChange(func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	})
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		a, r, err := c.Reconcile(ctx, s.Snapshot(), time.Now())
		if err != nil {
			c.Log.Warn("cloudflare ban list sync", "err", err)
		} else if a+r > 0 {
			c.Log.Info("cloudflare ban list synced", "added", a, "removed", r)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
			select { // coalesce bursts of bans
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}
