package users

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"slices"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Authentication methods.
const (
	MethodPassword   = "password"
	MethodOIDC       = "oidc"
	MethodBreakGlass = "breakglass"
	MethodToken      = "token" // personal access token (not a web session)
)

// Principal is the authenticated user behind a request.
type Principal struct {
	User      *User
	SessionID string // display ID (safe to show)
	Method    string
	Provider  string // OIDC provider slug for SSO sessions
	Listener  string
	AuthAt    time.Time
	MFAAt     *time.Time
	ExpiresAt time.Time
	CSRFToken string
	// Limited is set while MFA verification or enrollment is still required; only the MFA endpoints may be used.
	Limited     bool
	NeedsEnroll bool
	// TokenID is the personal access token for Method token; TokenRole caps the user's roles.
	TokenID    string
	tokenHash  []byte
	csrfSecret []byte
}

// HasRole reports whether the principal holds a role.
func (p *Principal) HasRole(r string) bool { return p != nil && slices.Contains(p.User.Roles, r) }

// SessionMeta describes the client and where it connected.
type SessionMeta struct {
	IP        string
	UserAgent string
	Provider  string
	// Listener is "loopback" (the SSH-tunnel admin listener) or "public" (the Cloudflare-fronted dashboard host).
	Listener string
}

func hashSessionToken(token string) []byte {
	h := sha256.Sum256([]byte("pulse/session/v1|" + token))
	return h[:]
}

func csrfToken(secret []byte, displayID string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("pulse/csrf/v1|" + displayID))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyCSRF checks a request's CSRF token in constant time.
func (p *Principal) VerifyCSRF(token string) bool {
	if p.Method == MethodToken {
		return true // bearer tokens are not ambient credentials
	}
	return token != "" && hmac.Equal([]byte(token), []byte(csrfToken(p.csrfSecret, p.SessionID)))
}

func (svc *Service) createSession(ctx context.Context, tx *sql.Tx, userID, method string, mfaAt *time.Time, meta SessionMeta) (string, error) {
	token, err := RandomToken()
	if err != nil {
		return "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	var mfa sql.NullInt64
	if mfaAt != nil {
		mfa = sql.NullInt64{Int64: ms(*mfaAt), Valid: true}
	}
	if len(meta.UserAgent) > 256 {
		meta.UserAgent = meta.UserAgent[:256]
	}
	now := svc.opts.Now()
	_, err = tx.ExecContext(ctx, `INSERT INTO web_sessions (id_hash, display_id, user_id, csrf_secret, auth_method, auth_provider, listener,
		auth_at, mfa_at, created_at, last_seen_at, absolute_expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hashSessionToken(token), "ses_"+NewID(), userID, secret, method, meta.Provider, meta.Listener, ms(now), mfa, ms(now), ms(now),
		ms(now.Add(svc.opts.SessionAbsolute)), meta.IP, meta.UserAgent)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET last_login_at = ? WHERE id = ?", ms(now), userID); err != nil {
		return "", err
	}
	return token, nil
}

// CreateSessionTx starts a session for a user authenticated by an identity provider, inside the caller's
// transaction (the OIDC login commits identity, role sync and session together).
func (svc *Service) CreateSessionTx(ctx context.Context, tx *sql.Tx, userID, method string, meta SessionMeta) (string, error) {
	return svc.createSession(ctx, tx, userID, method, nil, meta)
}

// Authenticate resolves a session token to a principal, enforcing revocation, idle and absolute expiry and the
// listener the session was created on. It marks the session active (idle timer).
func (svc *Service) Authenticate(ctx context.Context, token, listener string) (*Principal, error) {
	return svc.authenticate(ctx, token, listener, true)
}

// Validate is Authenticate without touching the idle timer (long-lived SSE requests re-check with it).
func (svc *Service) Validate(ctx context.Context, token, listener string) (*Principal, error) {
	return svc.authenticate(ctx, token, listener, false)
}

func (svc *Service) authenticate(ctx context.Context, token, listener string, touch bool) (*Principal, error) {
	if token == "" || len(token) > 128 {
		return nil, ErrUnauthorized
	}
	h := hashSessionToken(token)
	var (
		display, userID, method, provider, lis, ip string
		secret                                     []byte
		authAt, created, lastSeen, absExp          int64
		mfaAt, revokedAt                           sql.NullInt64
	)
	err := svc.db.R().QueryRowContext(ctx, `SELECT display_id, user_id, csrf_secret, auth_method, auth_provider, listener, auth_at, mfa_at,
		created_at, last_seen_at, absolute_expires_at, ip, revoked_at FROM web_sessions WHERE id_hash = ?`, h).
		Scan(&display, &userID, &secret, &method, &provider, &lis, &authAt, &mfaAt, &created, &lastSeen, &absExp, &ip, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	now := svc.opts.Now()
	if revokedAt.Valid || !now.Before(store.FromMs(absExp)) || now.Sub(store.FromMs(lastSeen)) > svc.opts.SessionIdle || lis != listener {
		return nil, ErrUnauthorized
	}
	u, err := svc.Get(ctx, userID)
	if err != nil || !u.Enabled {
		return nil, ErrUnauthorized
	}
	if method != MethodOIDC {
		// SSO-mapped roles are maintained only by SSO logins, so they apply only to SSO sessions.
		u.Roles = rolesWithoutSource(u.Assignments, "sso")
	}
	p := &Principal{User: u, SessionID: display, Method: method, Provider: provider, Listener: lis, AuthAt: store.FromMs(authAt),
		ExpiresAt: store.FromMs(absExp), tokenHash: h, csrfSecret: secret}
	if mfaAt.Valid {
		t := store.FromMs(mfaAt.Int64)
		p.MFAAt = &t
	}
	switch method {
	case MethodPassword:
		p.NeedsEnroll = u.MFARequired && !u.MFAEnrolled
		p.Limited = p.NeedsEnroll || (u.MFAEnrolled && p.MFAAt == nil)
	case MethodBreakGlass:
		// Break-glass removed the lost factor; an account policy requires MFA for enrolls a new one first.
		p.NeedsEnroll = u.MFARequired && !u.MFAEnrolled
		p.Limited = p.NeedsEnroll
	}
	p.CSRFToken = csrfToken(secret, display)
	if touch && now.Sub(store.FromMs(lastSeen)) > time.Minute {
		_ = svc.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE web_sessions SET last_seen_at = ? WHERE id_hash = ?", ms(now), h)
			return err
		})
	}
	return p, nil
}

func rolesWithoutSource(as []RoleAssignment, source string) []string {
	out := []string{}
	for _, a := range as {
		if a.Source != source && !slices.Contains(out, a.Role) {
			out = append(out, a.Role)
		}
	}
	slices.Sort(out)
	return out
}

// RequireRecentAuth checks that the session authenticated within the reauth window, and with MFA when the user has
// a factor (or needMFA is set). Personal access tokens pass: creating one required recent authentication, and they
// work only on the loopback listener.
func (svc *Service) RequireRecentAuth(p *Principal, needMFA bool) error {
	if p == nil {
		return ErrUnauthorized
	}
	if p.Method == MethodToken {
		return nil
	}
	now := svc.opts.Now()
	if now.Sub(p.AuthAt) > svc.opts.ReauthWindow {
		return ErrReauthRequired
	}
	if (needMFA || p.User.MFAEnrolled) && p.Method == MethodPassword {
		if p.MFAAt == nil || now.Sub(*p.MFAAt) > svc.opts.ReauthWindow {
			return ErrMFARequired
		}
	}
	return nil
}

// rotateTx revokes p's session and issues a replacement for the same user, method and provider with the given
// authentication times. It fails when p's session was already revoked, so a session rotates only once.
func (svc *Service) rotateTx(ctx context.Context, tx *sql.Tx, p *Principal, authAt time.Time, mfaAt *time.Time, meta SessionMeta) (string, error) {
	res, err := tx.ExecContext(ctx, "UPDATE web_sessions SET revoked_at = ?, revoke_reason = 'rotated' WHERE id_hash = ? AND revoked_at IS NULL",
		ms(svc.opts.Now()), p.tokenHash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrUnauthorized
	}
	meta.Provider, meta.Listener = p.Provider, p.Listener
	token, err := svc.createSession(ctx, tx, p.User.ID, p.Method, mfaAt, meta)
	if err != nil {
		return "", err
	}
	var mfa sql.NullInt64
	if mfaAt != nil {
		mfa = sql.NullInt64{Int64: ms(*mfaAt), Valid: true}
	}
	_, err = tx.ExecContext(ctx, "UPDATE web_sessions SET auth_at = ?, mfa_at = ? WHERE id_hash = ?", ms(authAt), mfa, hashSessionToken(token))
	return token, err
}

// Logout revokes the session.
func (svc *Service) Logout(ctx context.Context, token string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE web_sessions SET revoked_at = ?, revoke_reason = 'logout' WHERE id_hash = ? AND revoked_at IS NULL",
			ms(svc.opts.Now()), hashSessionToken(token))
		return err
	})
}

func revokeUserSessions(ctx context.Context, tx *sql.Tx, userID, reason string, except []byte, now int64) error {
	if except == nil {
		except = []byte{}
	}
	_, err := tx.ExecContext(ctx, `UPDATE web_sessions SET revoked_at = ?, revoke_reason = ? WHERE user_id = ? AND revoked_at IS NULL AND id_hash != ?`,
		now, reason, userID, except)
	return err
}

// SessionInfo describes an active session.
type SessionInfo struct {
	ID        string    `json:"id"`
	Method    string    `json:"auth_method"`
	Provider  string    `json:"auth_provider,omitempty"`
	Listener  string    `json:"listener"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen_at"`
	ExpiresAt time.Time `json:"expires_at"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Current   bool      `json:"current"`
}

// ListSessions returns a user's active sessions.
func (svc *Service) ListSessions(ctx context.Context, userID, currentDisplayID string) ([]SessionInfo, error) {
	now := svc.opts.Now()
	rows, err := svc.db.R().QueryContext(ctx, `SELECT display_id, auth_method, auth_provider, listener, created_at, last_seen_at, absolute_expires_at,
		ip, user_agent FROM web_sessions WHERE user_id = ? AND revoked_at IS NULL AND absolute_expires_at > ? AND last_seen_at > ?
		ORDER BY last_seen_at DESC`, userID, ms(now), ms(now.Add(-svc.opts.SessionIdle)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionInfo{}
	for rows.Next() {
		var s SessionInfo
		var c, l, e int64
		if err := rows.Scan(&s.ID, &s.Method, &s.Provider, &s.Listener, &c, &l, &e, &s.IP, &s.UserAgent); err != nil {
			return nil, err
		}
		s.CreatedAt, s.LastSeen, s.ExpiresAt, s.Current = store.FromMs(c), store.FromMs(l), store.FromMs(e), s.ID == currentDisplayID
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeSession revokes one of a user's sessions by display ID.
func (svc *Service) RevokeSession(ctx context.Context, userID, displayID, reason string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE web_sessions SET revoked_at = ?, revoke_reason = ? WHERE display_id = ? AND user_id = ? AND revoked_at IS NULL",
			ms(svc.opts.Now()), reason, displayID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return Errf(KindNotFound, "session not found")
		}
		return nil
	})
}

// RevokeAllSessions revokes every session of a user.
func (svc *Service) RevokeAllSessions(ctx context.Context, userID, reason string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error { return revokeUserSessions(ctx, tx, userID, reason, nil, ms(svc.opts.Now())) })
}

// PurgeExpired deletes old sessions, pending enrollments, one-time tokens and stale throttles (periodic job).
func (svc *Service) PurgeExpired(ctx context.Context) error {
	now := svc.opts.Now()
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		day := ms(now.Add(-24 * time.Hour))
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"DELETE FROM web_sessions WHERE absolute_expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)", []any{day, day}},
			{"DELETE FROM user_totp_pending WHERE expires_at < ?", []any{ms(now)}},
			{"DELETE FROM one_time_tokens WHERE expires_at < ?", []any{ms(now.Add(-7 * 24 * time.Hour))}},
			{"DELETE FROM login_throttle WHERE updated_at < ?", []any{ms(now.Add(-throttleDecay))}},
			{"DELETE FROM oidc_auth_states WHERE expires_at < ?", []any{ms(now)}},
		} {
			if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
}
