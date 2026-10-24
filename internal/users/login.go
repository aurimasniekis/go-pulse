package users

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Throttling: after throttleFree failures the delay doubles per failure up to throttleMax; counters decay after a
// day without failures. There is no permanent lockout.
const (
	throttleFree  = 3
	throttleMax   = 15 * time.Minute
	throttleDecay = 24 * time.Hour
)

func ipKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "ip:" + ip
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return "ip:" + p.String()
	}
	return "ip:" + a.String()
}

func (svc *Service) throttled(ctx context.Context, keys ...string) (bool, error) {
	now := ms(svc.opts.Now())
	for _, k := range keys {
		var next int64
		err := svc.db.R().QueryRowContext(ctx, "SELECT next_allowed_at FROM login_throttle WHERE key = ?", k).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if next > now {
			return true, nil
		}
	}
	return false, nil
}

func (svc *Service) recordFailure(ctx context.Context, tx *sql.Tx, keys ...string) error {
	now := svc.opts.Now()
	for _, k := range keys {
		failures := int64(1)
		var f, updated int64
		err := tx.QueryRowContext(ctx, "SELECT failures, updated_at FROM login_throttle WHERE key = ?", k).Scan(&f, &updated)
		switch {
		case err == nil:
			if now.Sub(store.FromMs(updated)) < throttleDecay {
				failures = f + 1
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		next := now
		if failures > throttleFree {
			next = now.Add(min(time.Second<<min(failures-throttleFree-1, 20), throttleMax))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO login_throttle (key, failures, next_allowed_at, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET failures = excluded.failures, next_allowed_at = excluded.next_allowed_at, updated_at = excluded.updated_at`,
			k, failures, ms(next), ms(now)); err != nil {
			return err
		}
	}
	return nil
}

func clearThrottle(ctx context.Context, tx *sql.Tx, keys ...string) error {
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, "DELETE FROM login_throttle WHERE key = ?", k); err != nil {
			return err
		}
	}
	return nil
}

// LoginResult is a successful first factor. With MFA pending the session is Limited until VerifyMFA succeeds.
type LoginResult struct {
	Token      string
	Principal  *Principal
	MFAPending bool
	MustEnroll bool
}

// Login verifies an email and password. Unknown accounts, wrong passwords and disabled accounts all return
// ErrInvalidCredentials after the same work (a dummy hash for unknown accounts).
func (svc *Service) Login(ctx context.Context, email, password string, meta SessionMeta) (*LoginResult, error) {
	res, err := svc.login(ctx, email, password, meta)
	svc.observe(MethodPassword, err)
	return res, err
}

// Observe counts a login attempt of method with its outcome (OnLogin).
func (svc *Service) Observe(method string, err error) { svc.observe(method, err) }

func (svc *Service) observe(method string, err error) {
	switch {
	case err == nil:
		svc.opts.OnLogin(method, "ok")
	case errors.Is(err, ErrLocalLoginDisabled):
	case errors.Is(err, ErrThrottled):
		svc.opts.OnLogin(method, "throttled")
	case KindOf(err) == KindUnauthorized, KindOf(err) == KindForbidden:
		svc.opts.OnLogin(method, "failed")
	default:
		svc.opts.OnLogin(method, "error")
	}
}

func (svc *Service) login(ctx context.Context, emailIn, password string, meta SessionMeta) (*LoginResult, error) {
	if off, err := svc.LocalLoginDisabled(ctx); err != nil {
		return nil, err
	} else if off {
		return nil, ErrLocalLoginDisabled
	}
	acctKey, ipK := throttleKeyAccount(emailIn), ipKey(meta.IP)
	if t, err := svc.throttled(ctx, acctKey, ipK); err != nil {
		return nil, err
	} else if t {
		return nil, ErrThrottled
	}
	pw, perr := NormalizePassword(password, 1)
	email, eerr := NormalizeEmail(emailIn)
	var userID, phc string
	var params int64
	enabled, found := false, false
	if eerr == nil {
		err := svc.db.R().QueryRowContext(ctx, `SELECT u.id, u.enabled, p.password_phc, p.params_version FROM users u
			JOIN user_passwords p ON p.user_id = u.id WHERE u.email = ?`, email).Scan(&userID, &enabled, &phc, &params)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	if !found {
		phc = dummyHash()
	}
	if perr != nil {
		pw = []byte("invalid")
	}
	if err := svc.acquire(ctx); err != nil {
		return nil, err
	}
	ok, verr := VerifyPassword(pw, phc)
	svc.release()
	if success := found && ok && verr == nil && perr == nil && enabled; !success {
		err := svc.db.Write(ctx, func(tx *sql.Tx) error {
			if err := svc.recordFailure(ctx, tx, acctKey, ipK); err != nil {
				return err
			}
			return audit.Insert(ctx, tx, audit.Actor{Name: "anonymous", IP: meta.IP}, "user.login", "", audit.Denied,
				map[string]any{"method": MethodPassword, "listener": meta.Listener})
		})
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}
	var token string
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := clearThrottle(ctx, tx, acctKey); err != nil {
			return err
		}
		if params != int64(CurrentParams.Version) {
			if fresh, err := HashPassword(pw, CurrentParams); err == nil {
				if _, err := tx.ExecContext(ctx, "UPDATE user_passwords SET password_phc = ?, params_version = ? WHERE user_id = ?",
					fresh, CurrentParams.Version, userID); err != nil {
					return err
				}
			}
		}
		var err error
		if token, err = svc.createSession(ctx, tx, userID, MethodPassword, nil, meta); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.Actor{Name: "user:" + email, IP: meta.IP}, "user.login", "user:"+userID, audit.OK,
			map[string]any{"method": MethodPassword, "listener": meta.Listener})
	})
	if err != nil {
		return nil, err
	}
	p, err := svc.Authenticate(ctx, token, meta.Listener)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, Principal: p, MFAPending: p.Limited && !p.NeedsEnroll, MustEnroll: p.NeedsEnroll}, nil
}

// VerifyMFA completes the second factor of a Limited password session with a TOTP or recovery code, then rotates
// the session.
func (svc *Service) VerifyMFA(ctx context.Context, p *Principal, code string, meta SessionMeta) (string, error) {
	if !p.Limited || p.NeedsEnroll || p.Method != MethodPassword {
		return "", ErrNoMFAPending
	}
	ok, factor, err := svc.verifySecondFactor(ctx, p.User.ID, code, meta.IP, "login")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", Errf(KindUnauthorized, "invalid code")
	}
	var token string
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := clearThrottle(ctx, tx, mfaKey(p.User.ID)); err != nil {
			return err
		}
		now := svc.opts.Now()
		var err error
		if token, err = svc.rotateTx(ctx, tx, p, now, &now, meta); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, meta.IP), "user.mfa.verify", "user:"+p.User.ID, audit.OK, map[string]any{"factor": factor})
	})
	return token, err
}

// checkSecondFactor verifies a TOTP code (no step reuse) or consumes a recovery code.
func (svc *Service) checkSecondFactor(ctx context.Context, userID, code string) (bool, string, error) {
	var enc []byte
	var last int64
	err := svc.db.R().QueryRowContext(ctx, "SELECT secret_enc, last_step FROM user_totp WHERE user_id = ?", userID).Scan(&enc, &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, "", err
	}
	if err == nil && len(code) == totpDigits {
		secret, err := svc.keys.Open(totpAAD(userID), enc)
		if err != nil {
			return false, "", err
		}
		step, ok := VerifyTOTP(secret, code, svc.opts.Now(), last)
		if !ok {
			return false, "", nil
		}
		var advanced int64
		err = svc.db.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, "UPDATE user_totp SET last_step = ? WHERE user_id = ? AND last_step < ?", step, userID, step)
			if err == nil {
				advanced, err = res.RowsAffected()
			}
			return err
		})
		return err == nil && advanced == 1, "totp", err
	}
	if len(code) >= 10 {
		var used int64
		err := svc.db.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, "UPDATE user_recovery_codes SET used_at = ? WHERE user_id = ? AND code_mac = ? AND used_at IS NULL",
				ms(svc.opts.Now()), userID, svc.recoveryMAC(userID, code))
			if err == nil {
				used, err = res.RowsAffected()
			}
			return err
		})
		return err == nil && used == 1, "recovery_code", err
	}
	return false, "", nil
}

// checkCurrentPassword verifies the signed-in user's password under the account throttle; failures are audited.
func (svc *Service) checkCurrentPassword(ctx context.Context, p *Principal, password, ip, purpose string) error {
	key := accountKey(p.User)
	if t, err := svc.throttled(ctx, key); err != nil {
		return err
	} else if t {
		return ErrThrottled
	}
	var phc string
	err := svc.db.R().QueryRowContext(ctx, "SELECT password_phc FROM user_passwords WHERE user_id = ?", p.User.ID).Scan(&phc)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidCredentials
	} else if err != nil {
		return err
	}
	pw, perr := NormalizePassword(password, 1)
	if perr != nil {
		pw = []byte("invalid")
	}
	if err := svc.acquire(ctx); err != nil {
		return err
	}
	ok, verr := VerifyPassword(pw, phc)
	svc.release()
	if ok && verr == nil && perr == nil {
		return nil
	}
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := svc.recordFailure(ctx, tx, key); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, ip), "user.password.check", "user:"+p.User.ID, audit.Denied, map[string]any{"purpose": purpose})
	})
	if err != nil {
		return err
	}
	return ErrInvalidCredentials
}

// Reauth refreshes the authentication time with the password (and the second factor when enrolled), then rotates.
func (svc *Service) Reauth(ctx context.Context, p *Principal, password, code string, meta SessionMeta) (string, error) {
	if p.Method != MethodPassword && p.Method != MethodBreakGlass {
		return "", Errf(KindPrecondition, "re-authenticate with your identity provider")
	}
	if err := svc.checkCurrentPassword(ctx, p, password, meta.IP, "reauth"); err != nil {
		return "", err
	}
	var mfaAt *time.Time
	if p.User.MFAEnrolled {
		ok, _, err := svc.verifySecondFactor(ctx, p.User.ID, code, meta.IP, "reauth")
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrMFARequired
		}
		now := svc.opts.Now()
		mfaAt = &now
	}
	var token string
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := clearThrottle(ctx, tx, accountKey(p.User), mfaKey(p.User.ID)); err != nil {
			return err
		}
		var err error
		if token, err = svc.rotateTx(ctx, tx, p, svc.opts.Now(), mfaAt, meta); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, meta.IP), "user.reauth", "user:"+p.User.ID, audit.OK, nil)
	})
	return token, err
}

// ChangePassword changes the user's own password (current password required) and revokes their other sessions.
func (svc *Service) ChangePassword(ctx context.Context, p *Principal, current, next, ip string) error {
	if err := svc.checkCurrentPassword(ctx, p, current, ip, "change"); err != nil {
		return err
	}
	phc, err := svc.hash(ctx, next)
	if err != nil {
		return err
	}
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := setPassword(ctx, tx, p.User.ID, phc, svc.opts.Now()); err != nil {
			return err
		}
		if err := clearThrottle(ctx, tx, accountKey(p.User)); err != nil {
			return err
		}
		if err := revokeUserSessions(ctx, tx, p.User.ID, "password_changed", p.tokenHash, ms(svc.opts.Now())); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, ip), "user.password.change", "user:"+p.User.ID, audit.OK, nil)
	})
}

func setPassword(ctx context.Context, tx *sql.Tx, userID, phc string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO user_passwords (user_id, password_phc, params_version, changed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET password_phc = excluded.password_phc, params_version = excluded.params_version, changed_at = excluded.changed_at`,
		userID, phc, CurrentParams.Version, ms(now))
	return err
}

// RecoveryResult is a redeemed break-glass recovery login.
type RecoveryResult struct {
	Token     string
	Principal *Principal
	// ResetToken lets the recovered user set a password they know (issued when the account has a password).
	ResetToken     string
	ResetExpiresAt time.Time
	MFAReset       bool
}

// RecoveryLogin exchanges a break-glass token (issued by `pulsed admin recover`) for a session. The token is single
// use. Recovery assumes the account's credentials are lost or compromised: its other sessions are revoked, the TOTP
// factor and recovery codes removed and throttles cleared, all in one transaction.
func (svc *Service) RecoveryLogin(ctx context.Context, token string, meta SessionMeta) (*RecoveryResult, error) {
	res, err := svc.recoveryLogin(ctx, token, meta)
	svc.observe(MethodBreakGlass, err)
	return res, err
}

func (svc *Service) recoveryLogin(ctx context.Context, token string, meta SessionMeta) (*RecoveryResult, error) {
	ipK := ipKey(meta.IP)
	if t, err := svc.throttled(ctx, ipK); err != nil {
		return nil, err
	} else if t {
		return nil, ErrThrottled
	}
	res := &RecoveryResult{}
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		*res = RecoveryResult{}
		if token == "" || len(token) > 128 {
			return ErrTokenInvalid
		}
		tok, err := svc.consumeToken(ctx, tx, "recovery_login", token)
		if err != nil {
			return err
		}
		u, err := svc.getTx(ctx, tx, tok.userID)
		if err != nil || !u.Enabled {
			return ErrTokenInvalid
		}
		now := svc.opts.Now()
		if err := revokeUserSessions(ctx, tx, u.ID, "breakglass", nil, ms(now)); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, "DELETE FROM user_totp WHERE user_id = ?", u.ID)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		res.MFAReset = n > 0
		for _, q := range []string{"DELETE FROM user_totp_pending WHERE user_id = ?", "DELETE FROM user_recovery_codes WHERE user_id = ?"} {
			if _, err := tx.ExecContext(ctx, q, u.ID); err != nil {
				return err
			}
		}
		if err := clearThrottle(ctx, tx, accountKey(u), mfaKey(u.ID)); err != nil {
			return err
		}
		if u.HasPassword {
			if res.ResetToken, res.ResetExpiresAt, err = svc.issueResetTx(ctx, tx, u.ID, audit.Actor{Name: "cli:breakglass"}); err != nil {
				return err
			}
		}
		if res.Token, err = svc.createSession(ctx, tx, u.ID, MethodBreakGlass, &now, meta); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(u, meta.IP), "user.breakglass.login", "user:"+u.ID, audit.OK,
			map[string]any{"mfa_reset": res.MFAReset, "password_reset_issued": res.ResetToken != ""})
	})
	if errors.Is(err, ErrTokenInvalid) {
		if ferr := svc.db.Write(ctx, func(tx *sql.Tx) error {
			if err := svc.recordFailure(ctx, tx, ipK); err != nil {
				return err
			}
			return audit.Insert(ctx, tx, audit.Actor{Name: "anonymous", IP: meta.IP}, "user.breakglass.login", "", audit.Denied, nil)
		}); ferr != nil {
			return nil, ferr
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if res.Principal, err = svc.Authenticate(ctx, res.Token, meta.Listener); err != nil {
		return nil, err
	}
	return res, nil
}

// ClearThrottle removes login throttling for a user (administrator action).
func (svc *Service) ClearThrottle(ctx context.Context, actor audit.Actor, userID string) error {
	u, err := svc.Get(ctx, userID)
	if err != nil {
		return err
	}
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := clearThrottle(ctx, tx, accountKey(u), mfaKey(u.ID)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.throttle.clear", "user:"+userID, audit.OK, nil)
	})
}
