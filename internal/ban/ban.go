// Package ban detects repeated authentication failures and bans the source (PL-BAN-001..009, ADR-0011).
//
// Failures come from NATS AUTH.ERR advisories, HTTP 401s and bad webhook signatures. A network (IPv4 /32, IPv6 /64)
// that fails Threshold times within Window is banned for Base × Escalation^previous-offences, capped at Max.
// Enforcement happens in the HTTP middleware, in nftables (banagent) and optionally in a Cloudflare IP list.
package ban

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Options tune detection.
type Options struct {
	Threshold     int
	Window        time.Duration
	Base          time.Duration
	Max           time.Duration
	Escalation    int
	OffenceMemory time.Duration
	// NeverBan are static networks that are never banned (config allowlist, host addresses). Loopback, private,
	// link-local and ULA ranges are always included; Cloudflare ranges are checked through IsCloudflare.
	NeverBan     []string
	IsCloudflare func(netip.Addr) bool
	Log          *slog.Logger
	Now          func() time.Time
}

// Defaults from the plan.
func (o *Options) defaults() {
	if o.Threshold <= 0 {
		o.Threshold = 5
	}
	if o.Window <= 0 {
		o.Window = 10 * time.Minute
	}
	if o.Base <= 0 {
		o.Base = time.Hour
	}
	if o.Max <= 0 {
		o.Max = 30 * 24 * time.Hour
	}
	if o.Escalation <= 1 {
		o.Escalation = 4
	}
	if o.OffenceMemory <= 0 {
		o.OffenceMemory = 30 * 24 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

var builtinNeverBan = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "0.0.0.0/8",
	"::1/128", "fc00::/7", "fe80::/10", "::/128"}

// Ban is an active or expired ban.
type Ban struct {
	Network   string    `json:"network"`
	Reason    string    `json:"reason"`
	Source    string    `json:"source" enum:"nats,http_token,webhook,manual"`
	Manual    bool      `json:"manual"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitzero" doc:"Absent: no expiry"`
	Offences  int       `json:"offences"`
}

// Failure is one recorded authentication failure (kept in memory for the Security page).
type Failure struct {
	Time    time.Time `json:"time"`
	IP      string    `json:"ip"`
	Source  string    `json:"source"`
	Detail  string    `json:"detail,omitempty"`
	Ignored string    `json:"ignored,omitempty" doc:"Why this failure cannot lead to a ban (never-ban network)"`
}

// Service is the detector and ban store.
type Service struct {
	db *store.DB
	o  Options

	mu       sync.Mutex
	windows  map[netip.Prefix][]time.Time
	active   map[netip.Prefix]time.Time // value: expiry (zero = none)
	allow    []netip.Prefix             // builtin + static + allowlist table
	static   []netip.Prefix
	recent   []Failure
	listener []func()
}

// New returns the service; call Load before use.
func New(db *store.DB, o Options) (*Service, error) {
	o.defaults()
	s := &Service{db: db, o: o, windows: map[netip.Prefix][]time.Time{}, active: map[netip.Prefix]time.Time{}}
	for _, c := range append(append([]string{}, builtinNeverBan...), o.NeverBan...) {
		p, err := ParseNetwork(c)
		if err != nil {
			return nil, err
		}
		s.static = append(s.static, p)
	}
	return s, nil
}

// OnChange registers a callback run after bans change (banagent long-poll wakeups, Cloudflare sync).
func (s *Service) OnChange(fn func()) {
	s.mu.Lock()
	s.listener = append(s.listener, fn)
	s.mu.Unlock()
}

func (s *Service) changed() {
	s.mu.Lock()
	ls := append([]func(){}, s.listener...)
	s.mu.Unlock()
	for _, fn := range ls {
		fn()
	}
}

// ParseNetwork parses an IP or CIDR into the canonical ban key: a single IPv4 address becomes /32, a single IPv6
// address becomes its /64 (a host controls its whole /64), explicit CIDRs keep their length.
func ParseNetwork(s string) (netip.Prefix, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return KeyOf(a), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("ban: %q is not an IP or CIDR", s)
	}
	if p.Addr().Is4In6() {
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	return p.Masked(), nil
}

// KeyOf maps an address to its ban key.
func KeyOf(a netip.Addr) netip.Prefix {
	a = a.Unmap()
	if a.Is4() {
		return netip.PrefixFrom(a, 32)
	}
	p, _ := a.Prefix(64)
	return p
}

func overlaps(list []netip.Prefix, p netip.Prefix) bool {
	for _, q := range list {
		if q.Overlaps(p) {
			return true
		}
	}
	return false
}

// neverBan explains why a network may not be banned ("" when it may).
func (s *Service) neverBan(p netip.Prefix) string {
	if overlaps(s.static, p) {
		return "never-ban network"
	}
	if overlaps(s.allow, p) {
		return "allowlisted"
	}
	if s.o.IsCloudflare != nil && s.o.IsCloudflare(p.Addr()) {
		return "Cloudflare range"
	}
	return ""
}

// Load reads active bans and the allowlist into memory.
func (s *Service) Load(ctx context.Context) error {
	active, err := s.loadActive(ctx)
	if err != nil {
		return err
	}
	allow, err := s.loadAllow(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.active, s.allow = active, allow
	s.mu.Unlock()
	return nil
}

func (s *Service) loadActive(ctx context.Context) (map[netip.Prefix]time.Time, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT network, expires_at FROM bans WHERE expires_at = 0 OR expires_at > ?", store.Ms(s.o.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	active := map[netip.Prefix]time.Time{}
	for rows.Next() {
		var n string
		var exp int64
		if err := rows.Scan(&n, &exp); err != nil {
			return nil, err
		}
		if p, err := netip.ParsePrefix(n); err == nil {
			active[p] = store.FromMs(exp)
		}
	}
	return active, rows.Err()
}

func (s *Service) loadAllow(ctx context.Context) ([]netip.Prefix, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT network FROM ban_allowlist")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []netip.Prefix
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if p, err := netip.ParsePrefix(n); err == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// IsBanned reports whether an address is currently banned.
func (s *Service) IsBanned(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	now := s.o.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, exp := range s.active {
		if p.Contains(a) && (exp.IsZero() || exp.After(now)) {
			return true
		}
	}
	return false
}

const recentMax = 500

// Fail records an authentication failure from ip and bans it when the threshold is crossed. Safe for concurrent use.
// The ban write is detached from ctx's cancellation: it must happen even when the failed request is already gone.
func (s *Service) Fail(ctx context.Context, ip, source, detail string) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return
	}
	key := KeyOf(a)
	now := s.o.Now()
	s.mu.Lock()
	why := s.neverBan(key)
	s.recent = append(s.recent, Failure{Time: now.UTC(), IP: a.Unmap().String(), Source: source, Detail: detail, Ignored: why})
	if len(s.recent) > recentMax {
		s.recent = s.recent[len(s.recent)-recentMax:]
	}
	if why != "" {
		s.mu.Unlock()
		return
	}
	if exp, banned := s.active[key]; banned && (exp.IsZero() || exp.After(now)) {
		s.mu.Unlock()
		return
	}
	w := s.windows[key]
	cut := now.Add(-s.o.Window)
	i := 0
	for i < len(w) && w[i].Before(cut) {
		i++
	}
	w = append(w[i:], now)
	trigger := len(w) >= s.o.Threshold
	if trigger {
		delete(s.windows, key)
	} else {
		s.windows[key] = w
	}
	if len(s.windows) > 100_000 { // bound memory under a spray from many addresses
		for k, v := range s.windows {
			if len(v) == 0 || v[len(v)-1].Before(cut) {
				delete(s.windows, k)
			}
		}
	}
	s.mu.Unlock()
	if trigger {
		if _, err := s.autoBan(context.WithoutCancel(ctx), key, source, fmt.Sprintf("%d %s failures in %s", s.o.Threshold, source, s.o.Window)); err != nil {
			s.o.Log.Error("ban", "network", key, "err", err)
		}
	}
}

// Duration returns the ban length for a network banned `previous` times recently.
func (s *Service) Duration(previous int) time.Duration {
	d := s.o.Base
	for range previous {
		d *= time.Duration(s.o.Escalation)
		if d >= s.o.Max {
			return s.o.Max
		}
	}
	return min(d, s.o.Max)
}

func (s *Service) autoBan(ctx context.Context, key netip.Prefix, source, reason string) (*Ban, error) {
	now := s.o.Now()
	var prev int
	var last int64
	err := s.db.R().QueryRowContext(ctx, "SELECT count, last_at FROM ban_offences WHERE network = ?", key.String()).Scan(&prev, &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if store.FromMs(last).Before(now.Add(-s.o.OffenceMemory)) {
		prev = 0
	}
	b := &Ban{Network: key.String(), Reason: reason, Source: source, Actor: "system", CreatedAt: now.UTC(),
		ExpiresAt: now.Add(s.Duration(prev)).UTC(), Offences: prev + 1}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := upsertBan(ctx, tx, b); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ban_offences(network, count, last_at) VALUES (?, ?, ?)
			ON CONFLICT(network) DO UPDATE SET count = excluded.count, last_at = excluded.last_at`, b.Network, b.Offences, store.Ms(b.ExpiresAt)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.System, "ban.auto", b.Network, audit.OK, map[string]any{"reason": reason, "source": source,
			"expires_at": b.ExpiresAt, "offence": b.Offences})
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.active[key] = b.ExpiresAt
	s.mu.Unlock()
	s.o.Log.Warn("banned", "network", b.Network, "until", b.ExpiresAt, "source", source, "offence", b.Offences)
	s.changed()
	return b, nil
}

func upsertBan(ctx context.Context, tx *sql.Tx, b *Ban) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO bans(network, reason, source, manual, actor, created_at, expires_at, offences)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(network) DO UPDATE SET reason = excluded.reason, source = excluded.source,
		manual = excluded.manual, actor = excluded.actor, created_at = excluded.created_at, expires_at = excluded.expires_at,
		offences = excluded.offences`, b.Network, b.Reason, b.Source, boolInt(b.Manual), b.Actor, store.Ms(b.CreatedAt), store.Ms(b.ExpiresAt), b.Offences)
	return err
}

// ErrNeverBan is returned when a manual ban targets a protected network.
var ErrNeverBan = errors.New("network may not be banned")

// Manual bans a network for d (0 = no expiry) on behalf of an operator (PL-BAN-005).
func (s *Service) Manual(ctx context.Context, network string, d time.Duration, reason string, actor audit.Actor) (*Ban, error) {
	key, err := ParseNetwork(network)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	why := s.neverBan(key)
	s.mu.Unlock()
	if why != "" {
		return nil, fmt.Errorf("%w: %s", ErrNeverBan, why)
	}
	now := s.o.Now()
	b := &Ban{Network: key.String(), Reason: reason, Source: "manual", Manual: true, Actor: actor.Name, CreatedAt: now.UTC(), Offences: 1}
	if d > 0 {
		b.ExpiresAt = now.Add(d).UTC()
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := upsertBan(ctx, tx, b); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "ban.manual", b.Network, audit.OK, map[string]any{"reason": reason, "expires_at": b.ExpiresAt})
	}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.active[key] = b.ExpiresAt
	s.mu.Unlock()
	s.changed()
	return b, nil
}

// ErrNotFound is returned for unknown bans.
var ErrNotFound = errors.New("ban not found")

// Unban lifts a ban (the offence history stays, so a repeat offender still escalates).
func (s *Service) Unban(ctx context.Context, network string, actor audit.Actor) error {
	key, err := ParseNetwork(network)
	if err != nil {
		return err
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE bans SET expires_at = ? WHERE network = ? AND (expires_at = 0 OR expires_at > ?)",
			store.Ms(s.o.Now()), key.String(), store.Ms(s.o.Now()))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit.Insert(ctx, tx, actor, "ban.lift", key.String(), audit.OK, nil)
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.active, key)
	s.mu.Unlock()
	s.changed()
	return nil
}

const banCols = "network, reason, source, manual, actor, created_at, expires_at, offences"

func scanBan(row interface{ Scan(...any) error }) (Ban, error) {
	var b Ban
	var manual int
	var c, e int64
	err := row.Scan(&b.Network, &b.Reason, &b.Source, &manual, &b.Actor, &c, &e, &b.Offences)
	b.Manual, b.CreatedAt, b.ExpiresAt = manual == 1, store.FromMs(c), store.FromMs(e)
	return b, err
}

// Active lists bans in force, newest first.
func (s *Service) Active(ctx context.Context) ([]Ban, error) {
	return s.query(ctx, "SELECT "+banCols+" FROM bans WHERE expires_at = 0 OR expires_at > ? ORDER BY created_at DESC", store.Ms(s.o.Now()))
}

// History lists expired bans from the last 30 days, newest first.
func (s *Service) History(ctx context.Context, limit int) ([]Ban, error) {
	return s.query(ctx, "SELECT "+banCols+" FROM bans WHERE expires_at != 0 AND expires_at <= ? ORDER BY expires_at DESC LIMIT ?",
		store.Ms(s.o.Now()), limit)
}

func (s *Service) query(ctx context.Context, q string, args ...any) ([]Ban, error) {
	rows, err := s.db.R().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ban{}
	for rows.Next() {
		b, err := scanBan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Recent returns recorded failures, newest first.
func (s *Service) Recent() []Failure {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Failure, len(s.recent))
	for i, f := range s.recent {
		out[len(s.recent)-1-i] = f
	}
	return out
}

// Offender is a network with recent bans. The offence memory runs from the end of its last ban, so a long ban does
// not erase its own history.
type Offender struct {
	Network string    `json:"network"`
	Count   int       `json:"count"`
	LastAt  time.Time `json:"last_at" doc:"When the last ban ended (or ends)"`
}

// Offenders lists networks banned within the offence memory, most frequent first.
func (s *Service) Offenders(ctx context.Context) ([]Offender, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT network, count, last_at FROM ban_offences WHERE last_at > ? ORDER BY count DESC, last_at DESC LIMIT 200",
		store.Ms(s.o.Now().Add(-s.o.OffenceMemory)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Offender{}
	for rows.Next() {
		var o Offender
		var last int64
		if err := rows.Scan(&o.Network, &o.Count, &last); err != nil {
			return nil, err
		}
		o.LastAt = store.FromMs(last)
		out = append(out, o)
	}
	return out, rows.Err()
}

// AllowEntry is an operator allowlist entry.
type AllowEntry struct {
	Network   string    `json:"network"`
	Note      string    `json:"note"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

// Allowlist returns the operator allowlist.
func (s *Service) Allowlist(ctx context.Context) ([]AllowEntry, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT network, note, actor, created_at FROM ban_allowlist ORDER BY network")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AllowEntry{}
	for rows.Next() {
		var e AllowEntry
		var c int64
		if err := rows.Scan(&e.Network, &e.Note, &e.Actor, &c); err != nil {
			return nil, err
		}
		e.CreatedAt = store.FromMs(c)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Allow adds a network to the allowlist and lifts bans inside it.
func (s *Service) Allow(ctx context.Context, network, note string, actor audit.Actor) error {
	p, err := netip.ParsePrefix(network)
	if err != nil {
		a, aerr := netip.ParseAddr(network)
		if aerr != nil {
			return fmt.Errorf("ban: %q is not an IP or CIDR", network)
		}
		p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
	}
	p = p.Masked()
	now := s.o.Now()
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ban_allowlist(network, note, actor, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(network) DO UPDATE SET note = excluded.note`, p.String(), note, actor.Name, store.Ms(now)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "ban.allow", p.String(), audit.OK, map[string]any{"note": note})
	})
	if err != nil {
		return err
	}
	var lifted []netip.Prefix
	s.mu.Lock()
	s.allow = append(s.allow, p)
	for k := range s.active {
		if k.Overlaps(p) {
			lifted = append(lifted, k)
		}
	}
	s.mu.Unlock()
	for _, k := range lifted {
		_ = s.Unban(ctx, k.String(), actor)
	}
	s.changed()
	return nil
}

// Disallow removes an allowlist entry.
func (s *Service) Disallow(ctx context.Context, network string, actor audit.Actor) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM ban_allowlist WHERE network = ?", network)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit.Insert(ctx, tx, actor, "ban.disallow", network, audit.OK, nil)
	})
	if err != nil {
		return err
	}
	allow, err := s.loadAllow(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.allow = allow
	s.mu.Unlock()
	return nil
}

// Prune drops in-memory expired bans and deletes rows (bans, offences) older than the offence memory (PL-DB-005).
func (s *Service) Prune(ctx context.Context) error {
	now := s.o.Now()
	s.mu.Lock()
	for k, exp := range s.active {
		if !exp.IsZero() && !exp.After(now) {
			delete(s.active, k)
		}
	}
	s.mu.Unlock()
	cut := store.Ms(now.Add(-s.o.OffenceMemory))
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM bans WHERE expires_at != 0 AND expires_at < ?", cut); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM ban_offences WHERE last_at < ?", cut)
		return err
	})
}

// Snapshot returns active bans as networks with expiries, sorted (for banagent and the Cloudflare sync).
func (s *Service) Snapshot() []Entry {
	now := s.o.Now()
	s.mu.Lock()
	out := make([]Entry, 0, len(s.active))
	for k, exp := range s.active {
		if exp.IsZero() || exp.After(now) {
			out = append(out, Entry{Network: k.String(), ExpiresAt: exp})
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Network < out[j].Network })
	return out
}

// Entry is one ban as banagent sees it.
type Entry struct {
	Network   string    `json:"network"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
