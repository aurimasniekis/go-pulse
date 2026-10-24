package users

import (
	"context"
	"database/sql"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
)

// ReauthExternal records a completed upstream step-up (the identity provider confirmed a fresh login): the session
// is rotated with a fresh authentication time.
func (svc *Service) ReauthExternal(ctx context.Context, p *Principal, provider string, meta SessionMeta) (string, error) {
	var token string
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if token, err = svc.rotateTx(ctx, tx, p, svc.opts.Now(), nil, meta); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, meta.IP), "user.reauth", "user:"+p.User.ID, audit.OK,
			map[string]any{"method": p.Method, "provider": provider})
	})
	return token, err
}

// ErrTOTPStepUpNotAllowed refuses the TOTP step-up fallback outside SSO sessions.
var ErrTOTPStepUpNotAllowed = Errf(KindPrecondition, "TOTP step-up is only available for SSO sessions whose provider allows it")

// ReauthTOTP is the step-up fallback for SSO sessions whose provider cannot prove a fresh login (no auth_time). Callers
// check that the session's provider allows it (oidc.Service.ReauthTOTP does).
func (svc *Service) ReauthTOTP(ctx context.Context, p *Principal, code string, meta SessionMeta) (string, error) {
	if p.Method != MethodOIDC || p.Limited {
		return "", ErrTOTPStepUpNotAllowed
	}
	if !p.User.MFAEnrolled {
		return "", Errf(KindPrecondition, "enroll TOTP in Pulse to re-authenticate")
	}
	ok, _, err := svc.verifySecondFactor(ctx, p.User.ID, code, meta.IP, "stepup")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrMFARequired
	}
	now := svc.opts.Now()
	var token string
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := clearThrottle(ctx, tx, mfaKey(p.User.ID)); err != nil {
			return err
		}
		var err error
		if token, err = svc.rotateTx(ctx, tx, p, now, &now, meta); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, meta.IP), "user.reauth", "user:"+p.User.ID, audit.OK,
			map[string]any{"method": p.Method, "factor": "totp"})
	})
	return token, err
}

// SetLocalLoginDisabledTx stores the SSO-only setting (internal/oidc checks the preconditions).
func (svc *Service) SetLocalLoginDisabledTx(ctx context.Context, tx *sql.Tx, disabled bool, by string) error {
	v := "false"
	if disabled {
		v = "true"
	}
	return PutSetting(ctx, tx, SettingLocalLoginDisabled, v, by, svc.opts.Now())
}

// RevokeSSOSessionsTx revokes a user's sessions inside tx (identity unlinked or provider removed).
func (svc *Service) RevokeSSOSessionsTx(ctx context.Context, tx *sql.Tx, userID, reason string) error {
	return revokeUserSessions(ctx, tx, userID, reason, nil, ms(svc.opts.Now()))
}

// CountEnabledAdmins counts enabled administrators.
func CountEnabledAdmins(ctx context.Context, q Querier) (int, error) {
	return countEnabledAdmins(ctx, q)
}

// GetTx returns a user inside a transaction.
func (svc *Service) GetTx(ctx context.Context, q Querier, id string) (*User, error) {
	return svc.getTx(ctx, q, id)
}

// InsertJITUserTx creates a just-in-time provisioned SSO user without roles (roles come from mappings).
func (svc *Service) InsertJITUserTx(ctx context.Context, tx *sql.Tx, email sql.NullString, name, by string) (string, error) {
	id := NewID()
	return id, insertUser(ctx, tx, id, email, name, "jit", false, by, svc.opts.Now())
}

// EmailTaken reports whether a user has this email.
func EmailTaken(ctx context.Context, q Querier, email string) (bool, error) {
	return emailTaken(ctx, q, email)
}

// ReplaceSSORolesTx replaces a user's SSO-sourced roles.
func ReplaceSSORolesTx(ctx context.Context, tx *sql.Tx, userID string, roles []string, refs map[string]string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_roles WHERE user_id = ? AND source = 'sso'", userID); err != nil {
		return err
	}
	for _, r := range roles {
		if err := insertRole(ctx, tx, userID, r, "sso", refs[r]); err != nil {
			return err
		}
	}
	return nil
}

// ListRolesTx returns a user's role assignments.
func ListRolesTx(ctx context.Context, q Querier, userID string) ([]RoleAssignment, error) {
	return listRoles(ctx, q, userID)
}

// TouchLogin sets last_login_at (SSO logins).
func TouchLogin(ctx context.Context, tx *sql.Tx, userID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, "UPDATE users SET last_login_at = ? WHERE id = ?", ms(now), userID)
	return err
}
