package users

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// One-time token lifetimes.
const (
	SetupTokenTTL    = 30 * time.Minute
	InviteTTL        = 72 * time.Hour
	ResetTTL         = time.Hour
	RecoveryLoginTTL = 30 * time.Minute
	LinkInviteTTL    = 72 * time.Hour
)

type oneTimeToken struct {
	kind, userID, data string
}

// IssueOneTimeTx stores a new one-time token (only its keyed MAC) and returns it.
func (svc *Service) IssueOneTimeTx(ctx context.Context, tx *sql.Tx, kind, userID string, ttl time.Duration, data map[string]any,
	actor audit.Actor) (string, time.Time, error) {
	token, err := RandomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := svc.opts.Now()
	exp := now.Add(ttl)
	if data == nil {
		data = map[string]any{}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO one_time_tokens (mac, kind, user_id, data, expires_at, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		svc.tokenMAC(token), kind, sql.NullString{String: userID, Valid: userID != ""}, mustJSON(data), ms(exp), ms(now), actor.Name)
	return token, exp, err
}

// ConsumeOneTimeTx validates a token of kind and marks it used. Unknown, expired, used and wrong-kind tokens all get
// ErrTokenInvalid.
func (svc *Service) ConsumeOneTimeTx(ctx context.Context, tx *sql.Tx, kind, token string) (userID, data string, err error) {
	t, err := svc.consumeToken(ctx, tx, kind, token)
	return t.userID, t.data, err
}

func (svc *Service) consumeToken(ctx context.Context, tx *sql.Tx, kind, token string) (oneTimeToken, error) {
	if token == "" || len(token) > 128 {
		return oneTimeToken{}, ErrTokenInvalid
	}
	mac := svc.tokenMAC(token)
	var t oneTimeToken
	var uid sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT kind, user_id, data FROM one_time_tokens WHERE mac = ?", mac).Scan(&t.kind, &uid, &t.data)
	if errors.Is(err, sql.ErrNoRows) {
		return oneTimeToken{}, ErrTokenInvalid
	}
	if err != nil {
		return oneTimeToken{}, err
	}
	if t.kind != kind {
		return oneTimeToken{}, ErrTokenInvalid
	}
	res, err := tx.ExecContext(ctx, "UPDATE one_time_tokens SET used_at = ? WHERE mac = ? AND used_at IS NULL AND expires_at > ?",
		ms(svc.opts.Now()), mac, ms(svc.opts.Now()))
	if err != nil {
		return oneTimeToken{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return oneTimeToken{}, ErrTokenInvalid
	}
	t.userID = uid.String
	return t, nil
}

// IssueSetupToken creates the one-time bootstrap token (`pulsed admin bootstrap`). It fails once any administrator
// exists, so there is never a default password or a hidden administrator.
func (svc *Service) IssueSetupToken(ctx context.Context) (string, time.Time, error) {
	var token string
	var exp time.Time
	actor := audit.Actor{Name: "cli:bootstrap"}
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		n, err := countEnabledAdmins(ctx, tx)
		if err != nil {
			return err
		}
		if n > 0 {
			return Errf(KindPrecondition, "an administrator already exists; use `pulsed admin recover` for lost access")
		}
		if token, exp, err = svc.IssueOneTimeTx(ctx, tx, "setup", "", SetupTokenTTL, nil, actor); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.setup_token.issue", "", audit.OK, nil)
	})
	return token, exp, err
}

// BootstrapInput creates the first administrator.
type BootstrapInput struct {
	Token       string `json:"token"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Password    string `json:"password"`
}

// Bootstrap consumes the setup token and creates the first administrator, who enrolls TOTP at first login.
func (svc *Service) Bootstrap(ctx context.Context, in BootstrapInput, ip string) (*User, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	phc, err := svc.hash(ctx, in.Password)
	if err != nil {
		return nil, err
	}
	var id string
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := svc.consumeToken(ctx, tx, "setup", in.Token); err != nil {
			return err
		}
		if n, err := countEnabledAdmins(ctx, tx); err != nil {
			return err
		} else if n > 0 {
			return ErrTokenInvalid
		}
		id, err = svc.insertLocalUser(ctx, tx, email, in.DisplayName, phc, "bootstrap", []string{RoleAdministrator}, true,
			audit.Actor{Name: "user:" + email, IP: ip})
		return err
	})
	if err != nil {
		return nil, err
	}
	return svc.Get(ctx, id)
}

func (svc *Service) insertLocalUser(ctx context.Context, tx *sql.Tx, email, name, phc, via string, roles []string, mfa bool, actor audit.Actor) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = email
	}
	if len(name) > 128 {
		return "", Errf(KindInvalid, "display name too long")
	}
	if taken, err := emailTaken(ctx, tx, email); err != nil {
		return "", err
	} else if taken {
		return "", Errf(KindConflict, "a user with this email already exists")
	}
	id := NewID()
	now := svc.opts.Now()
	if err := insertUser(ctx, tx, id, sql.NullString{String: email, Valid: true}, name, via, mfa, actor.Name, now); err != nil {
		return "", err
	}
	for _, r := range roles {
		if err := insertRole(ctx, tx, id, r, "manual", ""); err != nil {
			return "", err
		}
	}
	if err := setPassword(ctx, tx, id, phc, now); err != nil {
		return "", err
	}
	return id, audit.Insert(ctx, tx, actor, "user.create", "user:"+id, audit.OK, map[string]any{"email": email, "roles": roles, "via": via})
}

// Invite creates a single-use invitation with preset roles.
func (svc *Service) Invite(ctx context.Context, actor audit.Actor, email string, roles []string) (string, time.Time, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := validRoles(roles); err != nil {
		return "", time.Time{}, err
	}
	var token string
	var exp time.Time
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if taken, err := emailTaken(ctx, tx, e); err != nil {
			return err
		} else if taken {
			return Errf(KindConflict, "a user with this email already exists")
		}
		var err error
		if token, exp, err = svc.IssueOneTimeTx(ctx, tx, "invite", "", InviteTTL, map[string]any{"email": e, "roles": roles}, actor); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.invite", "", audit.OK, map[string]any{"email": e, "roles": roles})
	})
	return token, exp, err
}

// AcceptInvitation creates the invited user with a password.
func (svc *Service) AcceptInvitation(ctx context.Context, token, displayName, password, ip string) (*User, error) {
	phc, err := svc.hash(ctx, password)
	if err != nil {
		return nil, err
	}
	var id string
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		t, err := svc.consumeToken(ctx, tx, "invite", token)
		if err != nil {
			return err
		}
		var data struct {
			Email string   `json:"email"`
			Roles []string `json:"roles"`
		}
		if err := json.Unmarshal([]byte(t.data), &data); err != nil {
			return err
		}
		id, err = svc.insertLocalUser(ctx, tx, data.Email, displayName, phc, "invite", data.Roles, false, audit.Actor{Name: "user:" + data.Email, IP: ip})
		return err
	})
	if err != nil {
		return nil, err
	}
	return svc.Get(ctx, id)
}

// IssueResetToken creates a single-use password-set link for a user (administrator action). It also lets an
// SSO-only or newly created user set a first password.
func (svc *Service) IssueResetToken(ctx context.Context, actor audit.Actor, userID string) (string, time.Time, error) {
	var token string
	var exp time.Time
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := svc.getTx(ctx, tx, userID); err != nil {
			return err
		}
		var err error
		if token, exp, err = svc.issueResetTx(ctx, tx, userID, actor); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.reset_token.issue", "user:"+userID, audit.OK, nil)
	})
	return token, exp, err
}

func (svc *Service) issueResetTx(ctx context.Context, tx *sql.Tx, userID string, actor audit.Actor) (string, time.Time, error) {
	if _, err := tx.ExecContext(ctx, "UPDATE one_time_tokens SET used_at = ? WHERE kind = 'reset' AND user_id = ? AND used_at IS NULL",
		ms(svc.opts.Now()), userID); err != nil {
		return "", time.Time{}, err
	}
	return svc.IssueOneTimeTx(ctx, tx, "reset", userID, ResetTTL, nil, actor)
}

// ResetPassword sets a new password with a reset token and revokes every session of the user.
func (svc *Service) ResetPassword(ctx context.Context, token, password, ip string) error {
	phc, err := svc.hash(ctx, password)
	if err != nil {
		return err
	}
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		t, err := svc.consumeToken(ctx, tx, "reset", token)
		if err != nil {
			return err
		}
		u, err := svc.getTx(ctx, tx, t.userID)
		if err != nil {
			return ErrTokenInvalid
		}
		now := svc.opts.Now()
		if err := setPassword(ctx, tx, u.ID, phc, now); err != nil {
			return err
		}
		if err := revokeUserSessions(ctx, tx, u.ID, "password_reset", nil, ms(now)); err != nil {
			return err
		}
		if err := clearThrottle(ctx, tx, accountKey(u), mfaKey(u.ID)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(u, ip), "user.password.reset", "user:"+u.ID, audit.OK, nil)
	})
}

// IssueRecoveryLogin is the break-glass path: reachable only from `pulsed admin recover` on the host (it needs the
// master key). With newAdminEmail set it creates a new administrator instead of recovering userID.
func (svc *Service) IssueRecoveryLogin(ctx context.Context, userID, newAdminEmail string) (string, time.Time, error) {
	var token string
	var exp time.Time
	actor := audit.Actor{Name: "cli:breakglass"}
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		target := userID
		if newAdminEmail != "" {
			email, err := NormalizeEmail(newAdminEmail)
			if err != nil {
				return err
			}
			pw, err := RandomToken()
			if err != nil {
				return err
			}
			phc, err := HashPassword([]byte(pw), CurrentParams)
			if err != nil {
				return err
			}
			if target, err = svc.insertLocalUser(ctx, tx, email, email, phc, "recovery", []string{RoleAdministrator}, true, actor); err != nil {
				return err
			}
		} else {
			u, err := svc.getTx(ctx, tx, userID)
			if err != nil {
				return err
			}
			if !u.Enabled {
				if _, err := tx.ExecContext(ctx, "UPDATE users SET enabled = 1, updated_at = ? WHERE id = ?", ms(svc.opts.Now()), userID); err != nil {
					return err
				}
			}
		}
		var err error
		if token, exp, err = svc.IssueOneTimeTx(ctx, tx, "recovery_login", target, RecoveryLoginTTL, nil, actor); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.breakglass.issue", "user:"+target, audit.OK, map[string]any{"new_admin": newAdminEmail != ""})
	})
	return token, exp, err
}

// Invitation is a pending invitation. ID is the hex MAC of the token, never the token itself.
type Invitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Roles     []string  `json:"roles"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
}

// ListInvitations returns pending invitations, newest first.
func (svc *Service) ListInvitations(ctx context.Context) ([]Invitation, error) {
	rows, err := svc.db.R().QueryContext(ctx, `SELECT mac, data, expires_at, created_at, created_by FROM one_time_tokens
		WHERE kind = 'invite' AND used_at IS NULL AND expires_at > ? ORDER BY created_at DESC`, ms(svc.opts.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var mac []byte
		var data string
		var exp, created int64
		var inv Invitation
		if err := rows.Scan(&mac, &data, &exp, &created, &inv.CreatedBy); err != nil {
			return nil, err
		}
		var d struct {
			Email string   `json:"email"`
			Roles []string `json:"roles"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		inv.ID, inv.Email, inv.Roles, inv.ExpiresAt, inv.CreatedAt = hex.EncodeToString(mac), d.Email, d.Roles, store.FromMs(exp), store.FromMs(created)
		if inv.Roles == nil {
			inv.Roles = []string{}
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvitation invalidates a pending invitation.
func (svc *Service) RevokeInvitation(ctx context.Context, actor audit.Actor, id string) error {
	mac, err := hex.DecodeString(id)
	if err != nil || len(mac) == 0 {
		return Errf(KindNotFound, "invitation not found")
	}
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		var data string
		err := tx.QueryRowContext(ctx, "SELECT data FROM one_time_tokens WHERE mac = ? AND kind = 'invite'", mac).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return Errf(KindNotFound, "invitation not found")
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE one_time_tokens SET used_at = ? WHERE mac = ? AND used_at IS NULL", ms(svc.opts.Now()), mac)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return Errf(KindNotFound, "invitation is no longer pending")
		}
		var d struct {
			Email string `json:"email"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		return audit.Insert(ctx, tx, actor, "user.invite.revoke", "", audit.OK, map[string]any{"email": d.Email})
	})
}
