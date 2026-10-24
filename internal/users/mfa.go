package users

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// RecoveryCodeCount is how many single-use recovery codes are issued.
const RecoveryCodeCount = 10

// TOTPPendingTTL bounds an enrollment in progress.
const TOTPPendingTTL = 15 * time.Minute

func totpAAD(userID string) []byte { return []byte("pulse/user_totp/v1|" + userID) }

func (svc *Service) recoveryMAC(userID, code string) []byte {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	return svc.keys.MAC("recovery-code", []byte(userID+"|"+norm))
}

// TOTPEnrollment is returned when enrollment starts; the secret is shown once for the authenticator app.
type TOTPEnrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"otpauth_uri"`
}

// enrollGate decides whether p may start or confirm TOTP enrollment:
//   - a Limited session may enroll only when policy requires a first factor (NeedsEnroll);
//   - a Limited session with a confirmed factor must verify it first (VerifyMFA);
//   - a full session replacing a factor needs recent authentication with MFA;
//   - a full session adding a first factor needs recent authentication.
func (svc *Service) enrollGate(p *Principal) error {
	switch {
	case p.Method == MethodToken:
		return Errf(KindForbidden, "manage your second factor in the dashboard")
	case p.Limited && p.NeedsEnroll:
		return nil
	case p.Limited:
		return ErrCompleteMFAFirst
	case p.User.MFAEnrolled:
		return svc.RequireRecentAuth(p, true)
	default:
		return svc.RequireRecentAuth(p, false)
	}
}

// BeginTOTP starts (or restarts) enrollment. The confirmed factor stays in force until ConfirmTOTP replaces it.
func (svc *Service) BeginTOTP(ctx context.Context, p *Principal) (*TOTPEnrollment, error) {
	if err := svc.enrollGate(p); err != nil {
		return nil, err
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := svc.keys.Seal(totpAAD(p.User.ID), secret)
	if err != nil {
		return nil, err
	}
	now := svc.opts.Now()
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_totp_pending (user_id, secret_enc, created_at, expires_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id) DO UPDATE SET secret_enc = excluded.secret_enc, created_at = excluded.created_at, expires_at = excluded.expires_at`,
			p.User.ID, sealed, ms(now), ms(now.Add(TOTPPendingTTL))); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, ""), "user.mfa.enroll_start", "user:"+p.User.ID, audit.OK,
			map[string]any{"replaces_factor": p.User.MFAEnrolled})
	})
	if err != nil {
		return nil, err
	}
	account := p.User.Email
	if account == "" {
		account = p.User.DisplayName
	}
	return &TOTPEnrollment{Secret: EncodeTOTPSecret(secret), URI: TOTPURI(secret, svc.opts.Issuer, account)}, nil
}

var errStartEnrollment = Errf(KindPrecondition, "start TOTP enrollment first")

// ConfirmTOTP completes a pending enrollment: the secret becomes the user's factor, fresh recovery codes are issued
// (returned once) and the user's other sessions are revoked. Enrollment never refreshes the authentication time: a
// Limited session stays Limited and finishes sign-in with VerifyMFA and a later code; a full session is rotated with
// mfa_at = now and the new token returned ("" when unchanged).
func (svc *Service) ConfirmTOTP(ctx context.Context, p *Principal, code string, meta SessionMeta) ([]string, string, error) {
	if err := svc.enrollGate(p); err != nil {
		return nil, "", err
	}
	uid := p.User.ID
	if t, err := svc.throttled(ctx, mfaKey(uid)); err != nil {
		return nil, "", err
	} else if t {
		return nil, "", ErrThrottled
	}
	var enc []byte
	var exp int64
	err := svc.db.R().QueryRowContext(ctx, "SELECT secret_enc, expires_at FROM user_totp_pending WHERE user_id = ?", uid).Scan(&enc, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", errStartEnrollment
	} else if err != nil {
		return nil, "", err
	}
	now := svc.opts.Now()
	if !now.Before(store.FromMs(exp)) {
		return nil, "", errStartEnrollment
	}
	secret, err := svc.keys.Open(totpAAD(uid), enc)
	if err != nil {
		return nil, "", err
	}
	step, ok := VerifyTOTP(secret, code, now, 0)
	if !ok {
		if err := svc.recordMFAFailure(ctx, uid, meta.IP, "enroll"); err != nil {
			return nil, "", err
		}
		return nil, "", Errf(KindInvalid, "invalid code; check your authenticator's clock")
	}
	codes, err := newRecoveryCodes()
	if err != nil {
		return nil, "", err
	}
	var token string
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM user_totp_pending WHERE user_id = ? AND secret_enc = ? AND expires_at > ?", uid, enc, ms(now))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errStartEnrollment
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_totp (user_id, secret_enc, confirmed_at, last_step) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id) DO UPDATE SET secret_enc = excluded.secret_enc, confirmed_at = excluded.confirmed_at, last_step = excluded.last_step`,
			uid, enc, ms(now), step); err != nil {
			return err
		}
		if err := svc.storeRecoveryCodes(ctx, tx, uid, codes); err != nil {
			return err
		}
		if err := clearThrottle(ctx, tx, mfaKey(uid)); err != nil {
			return err
		}
		if err := revokeUserSessions(ctx, tx, uid, "mfa_changed", p.tokenHash, ms(now)); err != nil {
			return err
		}
		if !p.Limited {
			if token, err = svc.rotateTx(ctx, tx, p, p.AuthAt, &now, meta); err != nil {
				return err
			}
		}
		return audit.Insert(ctx, tx, Actor(p.User, meta.IP), "user.mfa.enroll", "user:"+uid, audit.OK,
			map[string]any{"replaced_factor": p.User.MFAEnrolled})
	})
	if err != nil {
		return nil, "", err
	}
	return codes, token, nil
}

func (svc *Service) recordMFAFailure(ctx context.Context, uid, ip, purpose string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := svc.recordFailure(ctx, tx, mfaKey(uid)); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.Actor{Name: "user-id:" + uid, IP: ip}, "user.mfa.verify", "user:"+uid, audit.Denied,
			map[string]any{"purpose": purpose})
	})
}

// verifySecondFactor is the single entry point for second-factor checks: shared MFA throttle, TOTP (no step reuse)
// or a recovery code, and audited failures.
func (svc *Service) verifySecondFactor(ctx context.Context, uid, code, ip, purpose string) (bool, string, error) {
	if t, err := svc.throttled(ctx, mfaKey(uid)); err != nil {
		return false, "", err
	} else if t {
		return false, "", ErrThrottled
	}
	ok, method, err := svc.checkSecondFactor(ctx, uid, strings.TrimSpace(code))
	if err != nil {
		return false, "", err
	}
	if !ok {
		if err := svc.recordMFAFailure(ctx, uid, ip, purpose); err != nil {
			return false, "", err
		}
	}
	return ok, method, nil
}

// DisableTOTP removes the second factor (recent authentication with MFA). Refused when policy requires MFA.
func (svc *Service) DisableTOTP(ctx context.Context, p *Principal, ip string) error {
	if err := svc.RequireRecentAuth(p, true); err != nil {
		return err
	}
	if p.User.MFARequired {
		return Errf(KindForbidden, "MFA is mandatory for this account")
	}
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{"DELETE FROM user_totp WHERE user_id = ?", "DELETE FROM user_totp_pending WHERE user_id = ?",
			"DELETE FROM user_recovery_codes WHERE user_id = ?"} {
			if _, err := tx.ExecContext(ctx, q, p.User.ID); err != nil {
				return err
			}
		}
		if err := revokeUserSessions(ctx, tx, p.User.ID, "mfa_changed", p.tokenHash, ms(svc.opts.Now())); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, ip), "user.mfa.disable", "user:"+p.User.ID, audit.OK, nil)
	})
}

// RegenerateRecoveryCodes replaces all recovery codes (recent authentication with MFA).
func (svc *Service) RegenerateRecoveryCodes(ctx context.Context, p *Principal, ip string) ([]string, error) {
	if err := svc.RequireRecentAuth(p, true); err != nil {
		return nil, err
	}
	if !p.User.MFAEnrolled {
		return nil, Errf(KindPrecondition, "enroll TOTP first")
	}
	codes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if err := svc.storeRecoveryCodes(ctx, tx, p.User.ID, codes); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, Actor(p.User, ip), "user.recovery_codes.regenerate", "user:"+p.User.ID, audit.OK, nil)
	})
	return codes, err
}

// RecoveryCodesLeft returns how many unused recovery codes the user has.
func (svc *Service) RecoveryCodesLeft(ctx context.Context, userID string) (int, error) {
	var n int
	err := svc.db.R().QueryRowContext(ctx, "SELECT COUNT(*) FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL", userID).Scan(&n)
	return n, err
}

const recoveryAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func newRecoveryCodes() ([]string, error) {
	codes := make([]string, RecoveryCodeCount)
	for i := range codes {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		var sb strings.Builder
		for j, c := range b {
			if j == 6 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(c)%len(recoveryAlphabet)])
		}
		codes[i] = sb.String()
	}
	return codes, nil
}

func (svc *Service) storeRecoveryCodes(ctx context.Context, tx *sql.Tx, uid string, codes []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_recovery_codes WHERE user_id = ?", uid); err != nil {
		return err
	}
	now := ms(svc.opts.Now())
	for _, c := range codes {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_recovery_codes (user_id, code_mac, created_at) VALUES (?, ?, ?)",
			uid, svc.recoveryMAC(uid, c), now); err != nil {
			return err
		}
	}
	return nil
}
