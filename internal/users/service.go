package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Roles (PL-USR-003). Administrators can do everything; operators run the bus (clients, tokens, webhooks, streams,
// bans, alerts, backups); auditors read everything including the audit log; viewers read the dashboards.
const (
	RoleAdministrator = "administrator"
	RoleOperator      = "operator"
	RoleAuditor       = "auditor"
	RoleViewer        = "viewer"
)

// AllRoles lists valid roles, most privileged first.
var AllRoles = []string{RoleAdministrator, RoleOperator, RoleAuditor, RoleViewer}

// Options configure the service.
type Options struct {
	PasswordMinBytes int
	SessionIdle      time.Duration
	SessionAbsolute  time.Duration
	ReauthWindow     time.Duration
	// MFARequiredRoles forces TOTP enrollment for password accounts holding these roles.
	MFARequiredRoles []string
	Issuer           string
	// HashConcurrency bounds simultaneous Argon2id computations (memory/CPU guard).
	HashConcurrency int
	Now             func() time.Time
	// OnLogin counts login attempts: method (password, oidc, recovery, token) and result (ok, failed, throttled).
	OnLogin func(method, result string)
}

// Service manages users and their authentication.
type Service struct {
	db   *store.DB
	keys *Keys
	opts Options
	sem  chan struct{}
}

// New returns the service.
func New(db *store.DB, keys *Keys, o Options) *Service {
	if o.PasswordMinBytes <= 0 {
		o.PasswordMinBytes = 12
	}
	if o.SessionIdle <= 0 {
		o.SessionIdle = 30 * time.Minute
	}
	if o.SessionAbsolute <= 0 {
		o.SessionAbsolute = 12 * time.Hour
	}
	if o.ReauthWindow <= 0 {
		o.ReauthWindow = 5 * time.Minute
	}
	if o.MFARequiredRoles == nil {
		o.MFARequiredRoles = []string{RoleAdministrator, RoleOperator}
	}
	if o.Issuer == "" {
		o.Issuer = "Pulse"
	}
	if o.HashConcurrency <= 0 {
		o.HashConcurrency = 2
	}
	if o.Now == nil {
		o.Now = store.Now
	}
	if o.OnLogin == nil {
		o.OnLogin = func(string, string) {}
	}
	return &Service{db: db, keys: keys, opts: o, sem: make(chan struct{}, o.HashConcurrency)}
}

// Keys returns the derived keys (shared with internal/oidc).
func (svc *Service) Keys() *Keys { return svc.keys }

// DB returns the store (shared with internal/oidc).
func (svc *Service) DB() *store.DB { return svc.db }

// Now returns the service clock's time.
func (svc *Service) Now() time.Time { return svc.opts.Now() }

// User is an account (no secrets).
type User struct {
	ID             string           `json:"id"`
	Email          string           `json:"email,omitempty"`
	DisplayName    string           `json:"display_name"`
	Enabled        bool             `json:"enabled"`
	ProvisionedVia string           `json:"provisioned_via"`
	Roles          []string         `json:"roles"`
	Assignments    []RoleAssignment `json:"role_assignments"`
	HasPassword    bool             `json:"has_password"`
	MFAEnrolled    bool             `json:"mfa_enrolled"`
	MFARequired    bool             `json:"mfa_required"`
	Version        int64            `json:"version"`
	CreatedAt      time.Time        `json:"created_at"`
	LastLoginAt    *time.Time       `json:"last_login_at,omitempty"`
}

// RoleAssignment records where a role came from (manual or an SSO mapping).
type RoleAssignment struct {
	Role      string `json:"role"`
	Source    string `json:"source"`
	SourceRef string `json:"source_ref,omitempty"`
}

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// NormalizeEmail lower-cases and validates an email address.
func NormalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if len(e) > 254 {
		return "", Errf(KindInvalid, "email too long")
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || !strings.Contains(e, "@") {
		return "", Errf(KindInvalid, "invalid email address")
	}
	return e, nil
}

func validRoles(roles []string) error {
	for _, r := range roles {
		if !slices.Contains(AllRoles, r) {
			return Errf(KindInvalid, "unknown role %q", r)
		}
	}
	return nil
}

// NewID returns a new lowercase ULID.
func NewID() string { return strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String()) }

// Actor is the audit actor for a user.
func Actor(u *User, ip string) audit.Actor {
	if u == nil {
		return audit.Actor{Name: "anonymous", IP: ip}
	}
	if u.Email != "" {
		return audit.Actor{Name: "user:" + u.Email, IP: ip}
	}
	return audit.Actor{Name: "user:" + u.ID, IP: ip}
}

type userRow struct {
	id, email, name, via string
	enabled, mfaReq      bool
	version              int64
	created, lastLogin   int64
}

const userCols = `id, COALESCE(email, ''), display_name, enabled, provisioned_via, mfa_required, version, created_at, COALESCE(last_login_at, 0)`

func scanUser(r interface{ Scan(...any) error }) (userRow, error) {
	var u userRow
	err := r.Scan(&u.id, &u.email, &u.name, &u.enabled, &u.via, &u.mfaReq, &u.version, &u.created, &u.lastLogin)
	return u, err
}

func (svc *Service) hydrate(ctx context.Context, q Querier, r userRow) (*User, error) {
	u := &User{ID: r.id, Email: r.email, DisplayName: r.name, Enabled: r.enabled, ProvisionedVia: r.via, Version: r.version,
		CreatedAt: store.FromMs(r.created), Roles: []string{}, Assignments: []RoleAssignment{}}
	if r.lastLogin != 0 {
		t := store.FromMs(r.lastLogin)
		u.LastLoginAt = &t
	}
	as, err := listRoles(ctx, q, r.id)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		u.Assignments = append(u.Assignments, a)
		if !slices.Contains(u.Roles, a.Role) {
			u.Roles = append(u.Roles, a.Role)
		}
	}
	slices.Sort(u.Roles)
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_passwords WHERE user_id = ?", r.id).Scan(&n); err != nil {
		return nil, err
	}
	u.HasPassword = n > 0
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_totp WHERE user_id = ?", r.id).Scan(&n); err != nil {
		return nil, err
	}
	u.MFAEnrolled = n > 0
	u.MFARequired = r.mfaReq
	for _, role := range u.Roles {
		if u.HasPassword && slices.Contains(svc.opts.MFARequiredRoles, role) {
			u.MFARequired = true
		}
	}
	return u, nil
}

func listRoles(ctx context.Context, q Querier, id string) ([]RoleAssignment, error) {
	rows, err := q.QueryContext(ctx, "SELECT role, source, source_ref FROM user_roles WHERE user_id = ? ORDER BY role, source, source_ref", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoleAssignment{}
	for rows.Next() {
		var a RoleAssignment
		if err := rows.Scan(&a.Role, &a.Source, &a.SourceRef); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (svc *Service) getTx(ctx context.Context, q Querier, id string) (*User, error) {
	r, err := scanUser(q.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return svc.hydrate(ctx, q, r)
}

// Get returns a user.
func (svc *Service) Get(ctx context.Context, id string) (*User, error) {
	return svc.getTx(ctx, svc.db.R(), id)
}

// GetByEmail returns a user by email.
func (svc *Service) GetByEmail(ctx context.Context, email string) (*User, error) {
	r, err := scanUser(svc.db.R().QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE email = ?", strings.ToLower(email)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return svc.hydrate(ctx, svc.db.R(), r)
}

// List returns all users.
func (svc *Service) List(ctx context.Context) ([]*User, error) {
	rs, err := listUserRows(ctx, svc.db.R())
	if err != nil {
		return nil, err
	}
	out := make([]*User, 0, len(rs))
	for _, r := range rs {
		u, err := svc.hydrate(ctx, svc.db.R(), r)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

func listUserRows(ctx context.Context, q Querier) ([]userRow, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY display_name COLLATE NOCASE, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rs []userRow
	for rows.Next() {
		r, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, rows.Err()
}

// Count returns the number of users.
func (svc *Service) Count(ctx context.Context) (int, error) {
	var n int
	err := svc.db.R().QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// CreateInput creates a user. Passwords are never set by administrators: the user sets one through an invitation or
// a reset link, or signs in with SSO.
type CreateInput struct {
	Email       string   `json:"email,omitempty" doc:"Optional for SSO-only users"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
	MFARequired bool     `json:"mfa_required,omitempty" doc:"Require TOTP even for roles that do not need it"`
}

// Create adds a user with only the roles given.
func (svc *Service) Create(ctx context.Context, actor audit.Actor, in CreateInput) (*User, error) {
	var email sql.NullString
	if in.Email != "" {
		e, err := NormalizeEmail(in.Email)
		if err != nil {
			return nil, err
		}
		email = sql.NullString{String: e, Valid: true}
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" || len(name) > 128 {
		return nil, Errf(KindInvalid, "display name is required (at most 128 bytes)")
	}
	if err := validRoles(in.Roles); err != nil {
		return nil, err
	}
	id := NewID()
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		if email.Valid {
			if exists, err := emailTaken(ctx, tx, email.String); err != nil {
				return err
			} else if exists {
				return Errf(KindConflict, "a user with this email already exists")
			}
		}
		if err := insertUser(ctx, tx, id, email, name, "admin", in.MFARequired, actor.Name, svc.opts.Now()); err != nil {
			return err
		}
		for _, r := range slices.Compact(slices.Sorted(slices.Values(in.Roles))) {
			if err := insertRole(ctx, tx, id, r, "manual", ""); err != nil {
				return err
			}
		}
		return audit.Insert(ctx, tx, actor, "user.create", "user:"+id, audit.OK, map[string]any{"email": email.String, "roles": in.Roles})
	})
	if err != nil {
		return nil, err
	}
	return svc.Get(ctx, id)
}

func emailTaken(ctx context.Context, q Querier, email string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE email = ?", email).Scan(&n)
	return n > 0, err
}

func insertUser(ctx context.Context, tx *sql.Tx, id string, email sql.NullString, name, via string, mfa bool, by string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO users (id, email, display_name, enabled, provisioned_via, mfa_required, created_at, created_by, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)`, id, email, name, via, mfa, store.Ms(now), by, store.Ms(now))
	return err
}

func insertRole(ctx context.Context, tx *sql.Tx, id, role, source, ref string) error {
	_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO user_roles (user_id, role, source, source_ref) VALUES (?, ?, ?, ?)", id, role, source, ref)
	return err
}

// UpdateInput changes a user; nil fields are left unchanged.
type UpdateInput struct {
	DisplayName *string   `json:"display_name,omitempty"`
	Roles       *[]string `json:"roles,omitempty" doc:"Replaces the manually assigned roles; SSO-mapped roles are kept"`
	Enabled     *bool     `json:"enabled,omitempty"`
	MFARequired *bool     `json:"mfa_required,omitempty"`
}

// Update applies in. Removing the last enabled administrator (or, under SSO-only, the last administrator who can
// sign in with SSO) is refused. Any change of effective roles, and disabling, revokes the user's sessions: a reduction
// takes effect at once and an elevation is only granted to a new session.
func (svc *Service) Update(ctx context.Context, actor audit.Actor, id string, in UpdateInput) (*User, error) {
	if in.Roles != nil {
		if err := validRoles(*in.Roles); err != nil {
			return nil, err
		}
	}
	if in.DisplayName != nil {
		if n := strings.TrimSpace(*in.DisplayName); n == "" || len(n) > 128 {
			return nil, Errf(KindInvalid, "display name is required (at most 128 bytes)")
		}
	}
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		before, err := svc.getTx(ctx, tx, id)
		if err != nil {
			return err
		}
		guard, err := newSSOAdminGuard(ctx, tx)
		if err != nil {
			return err
		}
		admins, err := NewAdminGuard(ctx, tx)
		if err != nil {
			return err
		}
		now := store.Ms(svc.opts.Now())
		details := map[string]any{}
		if in.DisplayName != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE users SET display_name = ?, updated_at = ?, version = version + 1 WHERE id = ?",
				strings.TrimSpace(*in.DisplayName), now, id); err != nil {
				return err
			}
			details["display_name"] = strings.TrimSpace(*in.DisplayName)
		}
		if in.MFARequired != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE users SET mfa_required = ?, updated_at = ?, version = version + 1 WHERE id = ?", *in.MFARequired, now, id); err != nil {
				return err
			}
			details["mfa_required"] = *in.MFARequired
		}
		if in.Roles != nil {
			if _, err := tx.ExecContext(ctx, "DELETE FROM user_roles WHERE user_id = ? AND source = 'manual'", id); err != nil {
				return err
			}
			for _, r := range slices.Compact(slices.Sorted(slices.Values(*in.Roles))) {
				if err := insertRole(ctx, tx, id, r, "manual", ""); err != nil {
					return err
				}
			}
		}
		if in.Enabled != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE users SET enabled = ?, updated_at = ?, version = version + 1 WHERE id = ?", *in.Enabled, now, id); err != nil {
				return err
			}
			details["enabled"] = *in.Enabled
		}
		if err := admins.Check(ctx, tx); err != nil {
			return err
		}
		if err := guard.check(ctx, tx); err != nil {
			return err
		}
		after, err := svc.getTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !slices.Equal(before.Roles, after.Roles) {
			details["roles_before"], details["roles_after"] = before.Roles, after.Roles
		}
		if !slices.Equal(before.Roles, after.Roles) || (in.Enabled != nil && !*in.Enabled) {
			if err := revokeUserSessions(ctx, tx, id, "access_changed", nil, now); err != nil {
				return err
			}
		}
		return audit.Insert(ctx, tx, actor, "user.update", "user:"+id, audit.OK, details)
	})
	if err != nil {
		return nil, err
	}
	return svc.Get(ctx, id)
}

// Delete removes a user with their sessions, identities and tokens. The last enabled administrator cannot be deleted.
func (svc *Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		u, err := svc.getTx(ctx, tx, id)
		if err != nil {
			return err
		}
		guard, err := newSSOAdminGuard(ctx, tx)
		if err != nil {
			return err
		}
		admins, err := NewAdminGuard(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM one_time_tokens WHERE user_id = ?", id); err != nil {
			return err
		}
		if err := admins.Check(ctx, tx); err != nil {
			return err
		}
		if err := guard.check(ctx, tx); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.delete", "user:"+id, audit.OK, map[string]any{"email": u.Email})
	})
}

// countEnabledAdmins counts enabled users holding the administrator role from any source.
func countEnabledAdmins(ctx context.Context, q Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT u.id) FROM users u JOIN user_roles r ON r.user_id = u.id
		WHERE u.enabled = 1 AND r.role = 'administrator'`).Scan(&n)
	return n, err
}

// CountSSOAdmins counts enabled administrators with a linked identity on an enabled provider.
func CountSSOAdmins(ctx context.Context, q Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT u.id) FROM users u
		JOIN user_roles r ON r.user_id = u.id AND r.role = 'administrator'
		JOIN user_identities i ON i.user_id = u.id
		JOIN oidc_providers p ON p.id = i.provider_id AND p.enabled = 1
		WHERE u.enabled = 1`).Scan(&n)
	return n, err
}

// AdminGuard refuses a change that removes the last enabled administrator (it allows changes while none exists).
type AdminGuard struct{ before int }

// NewAdminGuard records the number of enabled administrators before a change.
func NewAdminGuard(ctx context.Context, q Querier) (AdminGuard, error) {
	n, err := countEnabledAdmins(ctx, q)
	return AdminGuard{before: n}, err
}

// Check fails with ErrLastAdmin when the change left no enabled administrator.
func (g AdminGuard) Check(ctx context.Context, q Querier) error {
	if g.before == 0 {
		return nil
	}
	n, err := countEnabledAdmins(ctx, q)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}

// SettingLocalLoginDisabled turns off password login (SSO-only). Break-glass recovery is unaffected.
const SettingLocalLoginDisabled = "local_login_disabled"

// GetSetting returns an auth setting ("" when unset).
func GetSetting(ctx context.Context, q Querier, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM auth_settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// PutSetting stores an auth setting.
func PutSetting(ctx context.Context, tx *sql.Tx, key, value, by string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO auth_settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key, value, store.Ms(now), by)
	return err
}

// LocalLoginDisabled reports whether SSO-only enforcement is on.
func (svc *Service) LocalLoginDisabled(ctx context.Context) (bool, error) {
	v, err := GetSetting(ctx, svc.db.R(), SettingLocalLoginDisabled)
	return v == "true", err
}

// SSOAdminsAtRisk reports whether SSO-only enforcement is on and no administrator can sign in with SSO.
func SSOAdminsAtRisk(ctx context.Context, q Querier) (bool, error) {
	n, on, err := ssoAdmins(ctx, q)
	return on && n == 0, err
}

func ssoAdmins(ctx context.Context, q Querier) (int, bool, error) {
	v, err := GetSetting(ctx, q, SettingLocalLoginDisabled)
	if err != nil || v != "true" {
		return 0, false, err
	}
	n, err := CountSSOAdmins(ctx, q)
	return n, true, err
}

// ssoAdminGuard refuses a change that removes the last SSO-capable administrator while SSO-only is on.
type ssoAdminGuard struct {
	on     bool
	before int
}

func newSSOAdminGuard(ctx context.Context, q Querier) (ssoAdminGuard, error) {
	n, on, err := ssoAdmins(ctx, q)
	return ssoAdminGuard{on: on, before: n}, err
}

func (g ssoAdminGuard) check(ctx context.Context, q Querier) error {
	if !g.on || g.before == 0 {
		return nil
	}
	n, err := CountSSOAdmins(ctx, q)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLastSSOAdmin
	}
	return nil
}

// hash normalizes and hashes a password under the concurrency limit.
func (svc *Service) hash(ctx context.Context, pw string) (string, error) {
	n, err := NormalizePassword(pw, svc.opts.PasswordMinBytes)
	if err != nil {
		return "", err
	}
	if err := svc.acquire(ctx); err != nil {
		return "", err
	}
	defer svc.release()
	return HashPassword(n, CurrentParams)
}

func (svc *Service) acquire(ctx context.Context) error {
	select {
	case svc.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (svc *Service) release() { <-svc.sem }

// RandomToken returns a 256-bit URL-safe random token.
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (svc *Service) tokenMAC(token string) []byte {
	return svc.keys.MAC("one-time-token", []byte(token))
}

func throttleKeyAccount(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "acct:" + hex.EncodeToString(h[:12])
}

func accountKey(u *User) string {
	if u.Email == "" {
		return "acct-user:" + u.ID
	}
	return throttleKeyAccount(u.Email)
}

func mfaKey(id string) string { return "mfa:" + id }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func ms(t time.Time) int64 { return store.Ms(t) }
