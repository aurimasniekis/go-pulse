// Package tokens manages HTTP bearer tokens for the public API (PL-ING-001, PL-ING-007).
//
// Format: pulse_<prefix>_<secret>, where prefix is 8 lowercase alphanumerics (stored, shown in the UI) and secret is
// 32 random bytes base64url. Only SHA-256(token) is stored; tokens carry 256 bits of entropy, so a slow hash adds nothing.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Errors.
var (
	ErrUnauthenticated = errors.New("invalid or missing token")
	ErrNotFound        = errors.New("token not found")
	ErrInvalid         = errors.New("invalid token request")
	// ErrActive refuses deleting a token that is neither revoked nor expired.
	ErrActive = errors.New("revoke the token first")
)

// DefaultExpiry for new tokens.
const DefaultExpiry = 365 * 24 * time.Hour

// Token is a stored token (never the secret).
type Token struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Prefix      string           `json:"prefix" doc:"First characters of the token, for recognizing it"`
	Grants      []natsauth.Grant `json:"grants"`
	CIDRs       []string         `json:"cidrs" doc:"Allowed source networks; empty means any"`
	ExpiresAt   time.Time        `json:"expires_at"`
	CreatedAt   time.Time        `json:"created_at"`
	LastUsedAt  time.Time        `json:"last_used_at,omitzero"`
	LastUsedIP  string           `json:"last_used_ip,omitempty"`
	RevokedAt   time.Time        `json:"revoked_at,omitzero"`
	Status      string           `json:"status" enum:"active,revoked,expired"`
}

// Service manages tokens.
type Service struct {
	db  *store.DB
	now func() time.Time

	mu       sync.Mutex
	lastSeen map[string]time.Time // throttles last_used writes
}

// New returns a token service.
func New(db *store.DB) *Service {
	return &Service{db: db, now: store.Now, lastSeen: map[string]time.Time{}}
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

const alnum = "abcdefghijklmnopqrstuvwxyz0123456789"

func randomPrefix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b)
}

func hash(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

// CreateInput describes a new token.
type CreateInput struct {
	Name        string
	Description string
	Grants      []natsauth.Grant
	CIDRs       []string
	ExpiresIn   time.Duration
}

func normalizeCIDRs(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			a, err := netip.ParseAddr(c)
			if err != nil {
				return nil, fmt.Errorf("%w: %q is not an IP or CIDR", ErrInvalid, c)
			}
			c = netip.PrefixFrom(a, a.BitLen()).String()
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a CIDR", ErrInvalid, c)
		}
		out = append(out, p.Masked().String())
	}
	return out, nil
}

// Create stores a token and returns it with the secret, which is never retrievable again.
func (s *Service) Create(ctx context.Context, in CreateInput, actor audit.Actor) (*Token, string, error) {
	if !nameRe.MatchString(in.Name) {
		return nil, "", fmt.Errorf("%w: name must match %s", ErrInvalid, nameRe)
	}
	for i, g := range in.Grants {
		if g.Action != natsauth.ActPublish && g.Action != natsauth.ActSubscribe && g.Action != natsauth.ActRequest {
			return nil, "", fmt.Errorf("%w: grant %d: HTTP tokens support publish, subscribe and request", ErrInvalid, i)
		}
		if err := g.Validate(); err != nil {
			return nil, "", fmt.Errorf("%w: grant %d: %w", ErrInvalid, i, err)
		}
	}
	cidrs, err := normalizeCIDRs(in.CIDRs)
	if err != nil {
		return nil, "", err
	}
	if in.ExpiresIn <= 0 {
		in.ExpiresIn = DefaultExpiry
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, "", err
	}
	now := s.now()
	t := &Token{ID: strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String()), Name: in.Name, Description: in.Description,
		Prefix: randomPrefix(), Grants: in.Grants, CIDRs: cidrs, ExpiresAt: now.Add(in.ExpiresIn).Truncate(time.Second),
		CreatedAt: now, Status: "active"}
	if t.Grants == nil {
		t.Grants = []natsauth.Grant{}
	}
	full := "pulse_" + t.Prefix + "_" + base64.RawURLEncoding.EncodeToString(secret)
	g, _ := json.Marshal(t.Grants)
	c, _ := json.Marshal(t.CIDRs)
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO http_tokens(id, name, description, prefix, secret_hash, grants, cidrs, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, t.Name, t.Description, t.Prefix, hash(full), string(g), string(c),
			store.Ms(t.ExpiresAt), store.Ms(now)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: a token named %q exists", ErrInvalid, t.Name)
			}
			return err
		}
		return audit.Insert(ctx, tx, actor, "token.create", t.ID, audit.OK, map[string]any{"name": t.Name, "prefix": t.Prefix, "grants": t.Grants})
	})
	if err != nil {
		return nil, "", err
	}
	return t, full, nil
}

// Revoke disables a token.
func (s *Service) Revoke(ctx context.Context, id string, actor audit.Actor) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE http_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", store.Ms(s.now()), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM http_tokens WHERE id = ?", id).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
			return nil // already revoked
		}
		return audit.Insert(ctx, tx, actor, "token.revoke", id, audit.OK, nil)
	})
}

// Delete removes a revoked or expired token so its name can be reused. An active token returns ErrActive.
func (s *Service) Delete(ctx context.Context, id string, actor audit.Actor) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		var name string
		var exp, revoked int64
		err := tx.QueryRowContext(ctx, "SELECT name, expires_at, COALESCE(revoked_at, 0) FROM http_tokens WHERE id = ?", id).Scan(&name, &exp, &revoked)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		case revoked == 0 && store.FromMs(exp).After(s.now()):
			return ErrActive
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM http_tokens WHERE id = ?", id); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "token.delete", id, audit.OK, map[string]any{"name": name})
	})
}

const cols = `id, name, description, prefix, grants, cidrs, expires_at, created_at, last_used_at, last_used_ip, COALESCE(revoked_at, 0)`

func (s *Service) scan(row interface{ Scan(...any) error }) (*Token, error) {
	var t Token
	var g, c string
	var exp, created, used, revoked int64
	if err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Prefix, &g, &c, &exp, &created, &used, &t.LastUsedIP, &revoked); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(g), &t.Grants)
	_ = json.Unmarshal([]byte(c), &t.CIDRs)
	if t.CIDRs == nil {
		t.CIDRs = []string{}
	}
	t.ExpiresAt, t.CreatedAt, t.LastUsedAt, t.RevokedAt = store.FromMs(exp), store.FromMs(created), store.FromMs(used), store.FromMs(revoked)
	switch {
	case revoked != 0:
		t.Status = "revoked"
	case !t.ExpiresAt.After(s.now()):
		t.Status = "expired"
	default:
		t.Status = "active"
	}
	return &t, nil
}

// List returns all tokens.
func (s *Service) List(ctx context.Context) ([]*Token, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT "+cols+" FROM http_tokens ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Token{}
	for rows.Next() {
		t, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns one token.
func (s *Service) Get(ctx context.Context, id string) (*Token, error) {
	t, err := s.scan(s.db.R().QueryRowContext(ctx, "SELECT "+cols+" FROM http_tokens WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// Authenticate checks a bearer token from ip. Every failure returns ErrUnauthenticated (no detail for the caller).
func (s *Service) Authenticate(ctx context.Context, bearer, ip string) (*Token, error) {
	bearer = strings.TrimSpace(bearer)
	rest, ok := strings.CutPrefix(bearer, "pulse_")
	if !ok || len(rest) < 10 || rest[8] != '_' {
		return nil, ErrUnauthenticated
	}
	var stored []byte
	t, err := s.scan(s.db.R().QueryRowContext(ctx, "SELECT "+cols+" FROM http_tokens WHERE prefix = ?", rest[:8]))
	if err != nil {
		_ = subtle.ConstantTimeCompare(hash(bearer), make([]byte, 32)) // keep timing similar for unknown prefixes
		return nil, ErrUnauthenticated
	}
	if err := s.db.R().QueryRowContext(ctx, "SELECT secret_hash FROM http_tokens WHERE id = ?", t.ID).Scan(&stored); err != nil {
		return nil, ErrUnauthenticated
	}
	if subtle.ConstantTimeCompare(hash(bearer), stored) != 1 || t.Status != "active" {
		return nil, ErrUnauthenticated
	}
	if len(t.CIDRs) > 0 {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			return nil, ErrUnauthenticated
		}
		allowed := false
		for _, c := range t.CIDRs {
			if p, err := netip.ParsePrefix(c); err == nil && p.Contains(a.Unmap()) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrUnauthenticated
		}
	}
	s.touch(ctx, t.ID, ip)
	return t, nil
}

// touch records last use at most once a minute per token, so busy tokens do not turn every request into a write.
func (s *Service) touch(ctx context.Context, id, ip string) {
	now := s.now()
	s.mu.Lock()
	if last, ok := s.lastSeen[id]; ok && now.Sub(last) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.lastSeen[id] = now
	s.mu.Unlock()
	_ = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE http_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?", store.Ms(now), ip, id)
		return err
	})
}
