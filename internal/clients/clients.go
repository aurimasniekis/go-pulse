// Package clients manages tool clients: records in SQLite, credentials, grants and the APP account JWT that carries
// one scoped signing key per client (PL-AUTH-002..012, ADR-0025).
package clients

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
)

// DefaultExpiry is the default lifetime of client credentials.
const DefaultExpiry = 365 * 24 * time.Hour

// Status values.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
	StatusExpired = "expired" // derived, never stored
)

// Errors.
var (
	ErrNotFound = errors.New("client not found")
	ErrConflict = errors.New("client was modified concurrently")
	ErrRevoked  = errors.New("client is revoked")
	ErrInvalid  = errors.New("invalid client")
)

// Client is a tool identity.
type Client struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Tags        []string         `json:"tags"`
	Grants      []natsauth.Grant `json:"grants"`
	UserKey     string           `json:"user_key"`
	SigningKey  string           `json:"signing_key"`
	Status      string           `json:"status"`
	ExpiresAt   time.Time        `json:"expires_at"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	RevokedAt   time.Time        `json:"revoked_at,omitzero"`
	Version     int64            `json:"version"`
}

// InboxPrefix is the client's private inbox prefix.
func (c *Client) InboxPrefix() string { return natsauth.InboxPrefix(c.ID) }

// Service implements client management.
type Service struct {
	db  *store.DB
	ks  *bootstrap.KeySet
	sys *sysapi.Client
	log *slog.Logger
	now func() time.Time

	pushMu sync.Mutex // serializes APP account pushes
}

// New returns a client service. sys may be nil in tests that do not push.
func New(db *store.DB, ks *bootstrap.KeySet, sys *sysapi.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: db, ks: ks, sys: sys, log: log, now: store.Now}
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// CreateInput is what an operator supplies for a new client.
type CreateInput struct {
	Name        string
	Description string
	Tags        []string
	Grants      []natsauth.Grant
	ExpiresIn   time.Duration // 0 means DefaultExpiry
}

func newID() string { return strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String()) }

func validateGrants(gs []natsauth.Grant) error {
	for i, g := range gs {
		if err := g.Validate(); err != nil {
			return fmt.Errorf("%w: grant %d: %w", ErrInvalid, i, err)
		}
	}
	return nil
}

// Create stores a new client, pushes the APP account and returns the .creds once (PL-AUTH-004).
func (s *Service) Create(ctx context.Context, in CreateInput, actor audit.Actor) (*Client, []byte, error) {
	if !nameRe.MatchString(in.Name) {
		return nil, nil, fmt.Errorf("%w: name must match %s", ErrInvalid, nameRe)
	}
	if err := validateGrants(in.Grants); err != nil {
		return nil, nil, err
	}
	if in.ExpiresIn <= 0 {
		in.ExpiresIn = DefaultExpiry
	}
	now := s.now()
	c := &Client{
		ID: newID(), Name: in.Name, Description: in.Description, Tags: nonNil(in.Tags), Grants: nonNilG(in.Grants),
		Status: StatusActive, ExpiresAt: now.Add(in.ExpiresIn).Truncate(time.Second), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	issued, err := natsauth.IssueClient(c.ID, c.Name, s.ks.Pub(s.ks.AppAccount), c.ExpiresAt)
	if err != nil {
		return nil, nil, err
	}
	c.UserKey, c.SigningKey = issued.UserKey, issued.SigningKey
	tags, _ := json.Marshal(c.Tags)
	grants, _ := json.Marshal(c.Grants)
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO clients(id, name, description, tags, grants, user_key, signing_key, status,
			expires_at, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			c.ID, c.Name, c.Description, string(tags), string(grants), c.UserKey, c.SigningKey, c.Status,
			store.Ms(c.ExpiresAt), store.Ms(now), store.Ms(now)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: a client named %q exists", ErrInvalid, c.Name)
			}
			return err
		}
		return audit.Insert(ctx, tx, actor, "client.create", c.ID, audit.OK, map[string]any{"name": c.Name, "grants": c.Grants})
	})
	if err != nil {
		return nil, nil, err
	}
	if err := s.push(ctx); err != nil {
		// Compensate: the creds were never shown, so remove the client rather than leave a dangling identity.
		_ = s.db.Write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "DELETE FROM clients WHERE id = ?", c.ID)
			return e
		})
		return nil, nil, fmt.Errorf("push APP account: %w", err)
	}
	return c, issued.Creds, nil
}

// UpdateGrants replaces a client's grants (optimistic concurrency on version) and pushes the account (PL-AUTH-009).
func (s *Service) UpdateGrants(ctx context.Context, id string, grants []natsauth.Grant, version int64, actor audit.Actor) (*Client, error) {
	if err := validateGrants(grants); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(nonNilG(grants))
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE clients SET grants = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND version = ? AND status = 'active'`, string(b), store.Ms(s.now()), id, version)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return s.whyNot(ctx, tx, id)
		}
		return audit.Insert(ctx, tx, actor, "client.grants", id, audit.OK, map[string]any{"grants": grants})
	})
	if err != nil {
		return nil, err
	}
	if err := s.push(ctx); err != nil {
		return nil, fmt.Errorf("grants saved but APP account push failed (retried by the sync loop): %w", err)
	}
	return s.Get(ctx, id)
}

// UpdateMeta changes name, description and tags.
func (s *Service) UpdateMeta(ctx context.Context, id, name, description string, tags []string, version int64, actor audit.Actor) (*Client, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: name must match %s", ErrInvalid, nameRe)
	}
	t, _ := json.Marshal(nonNil(tags))
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE clients SET name = ?, description = ?, tags = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND version = ?`, name, description, string(t), store.Ms(s.now()), id, version)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: a client named %q exists", ErrInvalid, name)
			}
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return s.whyNot(ctx, tx, id)
		}
		return audit.Insert(ctx, tx, actor, "client.update", id, audit.OK, map[string]any{"name": name})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) whyNot(ctx context.Context, tx *sql.Tx, id string) error {
	var status string
	err := tx.QueryRowContext(ctx, "SELECT status FROM clients WHERE id = ?", id).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	case status == StatusRevoked:
		return ErrRevoked
	default:
		return ErrConflict
	}
}

// Rotate issues new credentials for a client and revokes the previous user key (PL-AUTH-008).
func (s *Service) Rotate(ctx context.Context, id string, expiresIn time.Duration, actor audit.Actor) (*Client, []byte, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if c.Status == StatusRevoked {
		return nil, nil, ErrRevoked
	}
	if expiresIn <= 0 {
		expiresIn = DefaultExpiry
	}
	now := s.now()
	exp := now.Add(expiresIn).Truncate(time.Second)
	issued, err := natsauth.IssueClient(c.ID, c.Name, s.ks.Pub(s.ks.AppAccount), exp)
	if err != nil {
		return nil, nil, err
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE clients SET user_key = ?, signing_key = ?, expires_at = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND version = ? AND status = 'active'`, issued.UserKey, issued.SigningKey, store.Ms(exp), store.Ms(now), id, c.Version)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return s.whyNot(ctx, tx, id)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO client_revocations(user_key, client_id, revoked_at, jwt_expires_at)
			VALUES (?, ?, ?, ?)`, c.UserKey, c.ID, store.Ms(now), store.Ms(c.ExpiresAt)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "client.rotate", id, audit.OK, map[string]any{"old_user_key": c.UserKey, "new_user_key": issued.UserKey})
	})
	if err != nil {
		return nil, nil, err
	}
	if err := s.push(ctx); err != nil {
		return nil, nil, fmt.Errorf("credentials rotated but APP account push failed; retry rotation: %w", err)
	}
	s.kickUser(ctx, c.UserKey)
	nc, err := s.Get(ctx, id)
	return nc, issued.Creds, err
}

// Revoke disables a client permanently: its scoped key leaves the APP account, its user key is revoked, and its
// connections are kicked (PL-AUTH-007).
func (s *Service) Revoke(ctx context.Context, id string, actor audit.Actor) error {
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Status == StatusRevoked {
		return nil
	}
	now := s.now()
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE clients SET status = 'revoked', revoked_at = ?, updated_at = ?, version = version + 1
			WHERE id = ?`, store.Ms(now), store.Ms(now), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO client_revocations(user_key, client_id, revoked_at, jwt_expires_at)
			VALUES (?, ?, ?, ?)`, c.UserKey, c.ID, store.Ms(now), store.Ms(c.ExpiresAt)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "client.revoke", id, audit.OK, map[string]any{"name": c.Name})
	})
	if err != nil {
		return err
	}
	if err := s.push(ctx); err != nil {
		return fmt.Errorf("client revoked in the database but the APP account push failed (retried by the sync loop): %w", err)
	}
	s.kickUser(ctx, c.UserKey)
	return nil
}

// Kick disconnects every connection of a client (forces it to reconnect with current permissions). Returns the count.
func (s *Service) Kick(ctx context.Context, id string, actor audit.Actor) (int, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	n := s.kickUser(ctx, c.UserKey)
	_ = audit.Write(ctx, s.db, actor, "client.kick", id, audit.OK, map[string]any{"connections": n})
	return n, nil
}

func (s *Service) kickUser(ctx context.Context, userKey string) int {
	if s.sys == nil {
		return 0
	}
	conns, err := s.Connections(ctx, userKey)
	if err != nil {
		s.log.Warn("list connections for kick", "err", err)
		return 0
	}
	n := 0
	for _, c := range conns {
		if err := s.sys.Kick(ctx, c.Cid); err == nil {
			n++
		}
	}
	return n
}

// Connections lists live connections authenticated with userKey (PL-AUTH-010).
func (s *Service) Connections(ctx context.Context, userKey string) ([]sysapi.ConnInfo, error) {
	if s.sys == nil {
		return nil, nil
	}
	cz, err := s.sys.Connz(ctx, sysapi.ConnzOptions{Account: s.ks.Pub(s.ks.AppAccount), Auth: true, Subs: false, Limit: 1024})
	if err != nil {
		return nil, err
	}
	var out []sysapi.ConnInfo
	for _, c := range cz.Conns {
		if c.AuthorizedUser == userKey {
			out = append(out, c)
		}
	}
	return out, nil
}

const selectCols = `id, name, description, tags, grants, user_key, signing_key, status, expires_at, created_at, updated_at,
	COALESCE(revoked_at, 0), version`

func scan(row interface{ Scan(...any) error }, now time.Time) (*Client, error) {
	var c Client
	var tags, grants string
	var exp, created, updated, revoked int64
	if err := row.Scan(&c.ID, &c.Name, &c.Description, &tags, &grants, &c.UserKey, &c.SigningKey, &c.Status,
		&exp, &created, &updated, &revoked, &c.Version); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(tags), &c.Tags); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(grants), &c.Grants); err != nil {
		return nil, err
	}
	c.ExpiresAt, c.CreatedAt, c.UpdatedAt, c.RevokedAt = store.FromMs(exp), store.FromMs(created), store.FromMs(updated), store.FromMs(revoked)
	if c.Status == StatusActive && !c.ExpiresAt.After(now) {
		c.Status = StatusExpired
	}
	return &c, nil
}

// Get returns one client.
func (s *Service) Get(ctx context.Context, id string) (*Client, error) {
	c, err := scan(s.db.R().QueryRowContext(ctx, "SELECT "+selectCols+" FROM clients WHERE id = ?", id), s.now())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// List returns all clients ordered by name.
func (s *Service) List(ctx context.Context) ([]*Client, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT "+selectCols+" FROM clients ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Client
	now := s.now()
	for rows.Next() {
		c, err := scan(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AppAccountSpec derives the APP account content from the database: one scoped signer per active, unexpired
// client and every revocation whose JWT could still be valid.
func (s *Service) AppAccountSpec(ctx context.Context) (natsauth.AppAccountSpec, error) {
	spec := natsauth.AppAccountSpec{SigningKey: s.ks.Pub(s.ks.AppSigning)}
	now := s.now()
	rows, err := s.db.R().QueryContext(ctx, `SELECT id, grants, signing_key FROM clients WHERE status = 'active' AND expires_at > ? ORDER BY id`, store.Ms(now))
	if err != nil {
		return spec, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, grants, sk string
		if err := rows.Scan(&id, &grants, &sk); err != nil {
			return spec, err
		}
		var gs []natsauth.Grant
		if err := json.Unmarshal([]byte(grants), &gs); err != nil {
			return spec, err
		}
		spec.Clients = append(spec.Clients, natsauth.ClientScope{ClientID: id, SigningKey: sk, Grants: gs})
	}
	if err := rows.Err(); err != nil {
		return spec, err
	}
	rrows, err := s.db.R().QueryContext(ctx, `SELECT user_key, revoked_at FROM client_revocations WHERE jwt_expires_at > ? ORDER BY user_key`, store.Ms(now))
	if err != nil {
		return spec, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var uk string
		var at int64
		if err := rrows.Scan(&uk, &at); err != nil {
			return spec, err
		}
		spec.Revocations = append(spec.Revocations, natsauth.Revocation{UserKey: uk, At: store.FromMs(at)})
	}
	return spec, rrows.Err()
}

// AppAccountJWT encodes the desired APP account JWT.
func (s *Service) AppAccountJWT(ctx context.Context) (string, error) {
	spec, err := s.AppAccountSpec(ctx)
	if err != nil {
		return "", err
	}
	ac, err := natsauth.AppAccountClaims(s.ks.Pub(s.ks.AppAccount), spec)
	if err != nil {
		return "", err
	}
	return natsauth.EncodeAccount(ac, s.ks.MustKP(s.ks.OperatorSigning))
}

func (s *Service) push(ctx context.Context) error {
	_, err := s.Sync(ctx, true)
	return err
}

// Sync pushes the APP account JWT when the resolver's copy is missing or differs from the database (PL-KEY-005).
// With force it always pushes. It reports whether a push happened.
func (s *Service) Sync(ctx context.Context, force bool) (bool, error) {
	if s.sys == nil {
		return false, errors.New("clients: no system connection")
	}
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	desired, err := s.AppAccountJWT(ctx)
	if err != nil {
		return false, err
	}
	if !force {
		cur, err := s.sys.ClaimsLookup(ctx, s.ks.Pub(s.ks.AppAccount))
		if err != nil {
			return false, err
		}
		if cur != "" && natsauth.AccountEquivalent(cur, desired) {
			return false, nil
		}
	}
	if err := s.sys.ClaimsUpdate(ctx, desired); err != nil {
		return false, err
	}
	s.log.Info("pushed APP account JWT", "forced", force)
	return true, nil
}

// PruneRevocations drops revocations whose JWT has expired anyway.
func (s *Service) PruneRevocations(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM client_revocations WHERE jwt_expires_at <= ?", store.Ms(s.now()))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilG(g []natsauth.Grant) []natsauth.Grant {
	if g == nil {
		return []natsauth.Grant{}
	}
	return g
}
