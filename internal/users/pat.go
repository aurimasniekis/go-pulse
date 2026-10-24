package users

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Personal access tokens let the pulse CLI call the admin API as a user (PL-USR-012). They are accepted only on the
// loopback admin listener (so a leaked token is useless without SSH access to the host), carry one role that is
// capped by the user's current manual roles at every request, expire, and are stored as keyed MACs.
const (
	PATPrefix     = "pulse_pat_"
	PATDefaultTTL = 90 * 24 * time.Hour
	PATMaxTTL     = 366 * 24 * time.Hour
)

// PAT is a personal access token (no secret).
type PAT struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Role       string     `json:"role"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`
	Revoked    bool       `json:"revoked"`
}

var patNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)

const patAlnum = "abcdefghijklmnopqrstuvwxyz0123456789"

func (svc *Service) patMAC(tok string) []byte {
	return svc.keys.MAC("personal-access-token", []byte(tok))
}

// CreatePAT issues a token for p's user with role (one of the user's manual roles) and ttl. Recent authentication is
// required by the API layer. The token is returned once.
func (svc *Service) CreatePAT(ctx context.Context, p *Principal, name, role string, ttl time.Duration, ip string) (*PAT, string, error) {
	if p.Method == MethodToken {
		return nil, "", Errf(KindForbidden, "personal access tokens cannot create tokens")
	}
	actor := Actor(p.User, ip)
	if p.Method == "host" {
		actor = audit.Actor{Name: "cli:admin-token"}
	}
	name = strings.TrimSpace(name)
	if !patNameRe.MatchString(name) {
		return nil, "", Errf(KindInvalid, "name must be 1-64 letters, digits, spaces, '.', '_' or '-'")
	}
	manual := rolesWithoutSource(p.User.Assignments, "sso")
	if !slices.Contains(manual, role) {
		return nil, "", Errf(KindInvalid, "role must be one of your own roles (not SSO-mapped ones): %s", strings.Join(manual, ", "))
	}
	if ttl <= 0 {
		ttl = PATDefaultTTL
	}
	if ttl > PATMaxTTL {
		return nil, "", Errf(KindInvalid, "a token may live at most 366 days")
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	prefix := make([]byte, 8)
	for i, c := range b {
		prefix[i] = patAlnum[int(c)%len(patAlnum)]
	}
	secret, err := RandomToken()
	if err != nil {
		return nil, "", err
	}
	tok := PATPrefix + string(prefix) + "_" + secret
	now := svc.opts.Now()
	t := &PAT{ID: NewID(), UserID: p.User.ID, Name: name, Prefix: string(prefix), Role: role, ExpiresAt: now.Add(ttl), CreatedAt: now}
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_tokens (id, user_id, name, prefix, mac, role, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.UserID, t.Name, t.Prefix, svc.patMAC(tok), t.Role, ms(t.ExpiresAt), ms(now)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.token.create", "user_token:"+t.ID, audit.OK,
			map[string]any{"name": name, "role": role, "expires_at": t.ExpiresAt})
	})
	if err != nil {
		return nil, "", err
	}
	return t, tok, nil
}

const patCols = `id, user_id, name, prefix, role, expires_at, created_at, COALESCE(last_used_at, 0), last_used_ip, revoked_at IS NOT NULL`

func scanPAT(r interface{ Scan(...any) error }) (*PAT, error) {
	var t PAT
	var exp, created, used int64
	if err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Role, &exp, &created, &used, &t.LastUsedIP, &t.Revoked); err != nil {
		return nil, err
	}
	t.ExpiresAt, t.CreatedAt = store.FromMs(exp), store.FromMs(created)
	if used != 0 {
		u := store.FromMs(used)
		t.LastUsedAt = &u
	}
	return &t, nil
}

// ListPATs returns a user's tokens (all users when userID is "").
func (svc *Service) ListPATs(ctx context.Context, userID string) ([]*PAT, error) {
	q, args := "SELECT "+patCols+" FROM user_tokens ORDER BY created_at DESC", []any{}
	if userID != "" {
		q, args = "SELECT "+patCols+" FROM user_tokens WHERE user_id = ? ORDER BY created_at DESC", []any{userID}
	}
	rows, err := svc.db.R().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PAT{}
	for rows.Next() {
		t, err := scanPAT(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokePAT revokes a token. userID restricts it to the owner's tokens ("" for administrators).
func (svc *Service) RevokePAT(ctx context.Context, actor audit.Actor, userID, id string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		q, args := "UPDATE user_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", []any{ms(svc.opts.Now()), id}
		if userID != "" {
			q, args = q+" AND user_id = ?", append(args, userID)
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return Errf(KindNotFound, "token not found")
		}
		return audit.Insert(ctx, tx, actor, "user.token.revoke", "user_token:"+id, audit.OK, nil)
	})
}

// AuthenticatePAT resolves a bearer token to a principal. Its roles are the token's role if the user still holds it
// as a manual role, else none.
func (svc *Service) AuthenticatePAT(ctx context.Context, bearer, ip string) (*Principal, error) {
	if !strings.HasPrefix(bearer, PATPrefix) || len(bearer) > 128 {
		return nil, ErrUnauthorized
	}
	t, err := scanPAT(svc.db.R().QueryRowContext(ctx, "SELECT "+patCols+" FROM user_tokens WHERE mac = ?", svc.patMAC(bearer)))
	if errors.Is(err, sql.ErrNoRows) {
		svc.observe(MethodToken, ErrUnauthorized)
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	now := svc.opts.Now()
	if t.Revoked || !now.Before(t.ExpiresAt) {
		svc.observe(MethodToken, ErrUnauthorized)
		return nil, ErrUnauthorized
	}
	u, err := svc.Get(ctx, t.UserID)
	if err != nil || !u.Enabled {
		return nil, ErrUnauthorized
	}
	u.Roles = []string{}
	if slices.Contains(rolesWithoutSource(u.Assignments, "sso"), t.Role) {
		u.Roles = []string{t.Role}
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute || t.LastUsedIP != ip {
		_ = svc.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE user_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?", ms(now), ip, t.ID)
			return err
		})
	}
	return &Principal{User: u, SessionID: "pat_" + t.ID, Method: MethodToken, Listener: "loopback", AuthAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		TokenID: t.ID}, nil
}

// IssuePATOnHost issues a personal access token from `pulsed admin token` (host shell with the master key, the same
// trust as break-glass). With create, a missing user is created without a password, so it can only use tokens. The
// role is added to the user's manual roles when missing.
func (svc *Service) IssuePATOnHost(ctx context.Context, email, name, role string, ttl time.Duration, create bool) (*PAT, string, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, "", err
	}
	if !slices.Contains(AllRoles, role) {
		return nil, "", Errf(KindInvalid, "unknown role %q", role)
	}
	if ttl <= 0 {
		ttl = PATDefaultTTL
	}
	if ttl > PATMaxTTL {
		return nil, "", Errf(KindInvalid, "a token may live at most 366 days")
	}
	name = strings.TrimSpace(name)
	if !patNameRe.MatchString(name) {
		return nil, "", Errf(KindInvalid, "name must be 1-64 letters, digits, spaces, '.', '_' or '-'")
	}
	actor := audit.Actor{Name: "cli:admin-token"}
	var uid string
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email = ?", email).Scan(&uid)
		switch {
		case errors.Is(err, sql.ErrNoRows) && create:
			uid = NewID()
			if err := insertUser(ctx, tx, uid, sql.NullString{String: email, Valid: true}, email, "admin", false, actor.Name, svc.opts.Now()); err != nil {
				return err
			}
			if err := audit.Insert(ctx, tx, actor, "user.create", "user:"+uid, audit.OK, map[string]any{"email": email, "via": "admin-token"}); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		}
		return insertRole(ctx, tx, uid, role, "manual", "")
	})
	if err != nil {
		return nil, "", err
	}
	u, err := svc.Get(ctx, uid)
	if err != nil {
		return nil, "", err
	}
	if !u.Enabled {
		return nil, "", Errf(KindPrecondition, "user %s is disabled", email)
	}
	return svc.CreatePAT(ctx, &Principal{User: u, Method: "host"}, name, role, ttl, "")
}
