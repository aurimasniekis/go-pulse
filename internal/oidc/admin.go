package oidc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// IssueLinkInvite creates a single-use invitation that links the next SSO login through providerID to userID
// (recent authentication required by the API layer). The token is shown once.
func (svc *Service) IssueLinkInvite(ctx context.Context, actor audit.Actor, userID, providerID string) (string, time.Time, error) {
	var token string
	var exp time.Time
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := svc.auth.GetTx(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := svc.providerWhere(ctx, tx, "id = ?", providerID); err != nil {
			return err
		}
		var err error
		if token, exp, err = svc.auth.IssueOneTimeTx(ctx, tx, "link", userID, users.LinkInviteTTL, map[string]any{"provider_id": providerID}, actor); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.identity.link_invite", "user:"+userID, audit.OK, map[string]any{"provider_id": providerID})
	})
	return token, exp, err
}

// checkLinkToken consumes a link invitation for providerID and returns its user.
func (svc *Service) checkLinkToken(ctx context.Context, token, providerID string) (string, error) {
	var uid string
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		u, data, err := svc.auth.ConsumeOneTimeTx(ctx, tx, "link", token)
		if err != nil {
			return err
		}
		var d struct {
			ProviderID string `json:"provider_id"`
		}
		if json.Unmarshal([]byte(data), &d) != nil || d.ProviderID != providerID || u == "" {
			return users.ErrTokenInvalid
		}
		uid = u
		return nil
	})
	return uid, err
}

// LinkIdentity links an expected identity (the provider's issuer + subject) to a user directly.
func (svc *Service) LinkIdentity(ctx context.Context, actor audit.Actor, userID, providerID, subject string) error {
	if subject == "" || len(subject) > 255 {
		return invalid("subject is required (at most 255 bytes)")
	}
	p, err := svc.GetProvider(ctx, providerID)
	if err != nil {
		return err
	}
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := svc.auth.GetTx(ctx, tx, userID); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRowContext(ctx, "SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?", p.Issuer, subject).Scan(&existing)
		if err == nil {
			if existing == userID {
				return nil
			}
			return users.Errf(users.KindConflict, "this identity is already linked to another user")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_identities (issuer, subject, provider_id, user_id, linked_via, created_at)
			VALUES (?, ?, ?, ?, 'admin', ?)`, p.Issuer, subject, p.ID, userID, store.Ms(svc.now())); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.identity.link", "user:"+userID, audit.OK, map[string]any{"provider": p.Slug, "via": "admin"})
	})
}

// UnlinkIdentity removes a linked identity, drops the user's SSO roles and revokes their sessions.
func (svc *Service) UnlinkIdentity(ctx context.Context, actor audit.Actor, userID, issuer, subject string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		admins, err := users.NewAdminGuard(ctx, tx)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM user_identities WHERE issuer = ? AND subject = ? AND user_id = ?", issuer, subject, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return users.Errf(users.KindNotFound, "identity not found")
		}
		if err := users.ReplaceSSORolesTx(ctx, tx, userID, nil, nil); err != nil {
			return err
		}
		if err := admins.Check(ctx, tx); err != nil {
			return err
		}
		if err := svc.checkSSOOnlyStillSafe(ctx, tx); err != nil {
			return err
		}
		if err := svc.auth.RevokeSSOSessionsTx(ctx, tx, userID, "identity_unlinked"); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "user.identity.unlink", "user:"+userID, audit.OK, map[string]any{"issuer": issuer})
	})
}

// Identities lists a user's linked identities.
func (svc *Service) Identities(ctx context.Context, userID string) ([]Identity, error) {
	rows, err := svc.db.R().QueryContext(ctx, `SELECT issuer, subject, provider_id, user_id, linked_via, created_at, COALESCE(last_login_at, 0)
		FROM user_identities WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Identity{}
	for rows.Next() {
		var i Identity
		var c, l int64
		if err := rows.Scan(&i.Issuer, &i.Subject, &i.ProviderID, &i.UserID, &i.LinkedVia, &c, &l); err != nil {
			return nil, err
		}
		i.CreatedAt = store.FromMs(c)
		if l != 0 {
			t := store.FromMs(l)
			i.LastLoginAt = &t
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ErrSSOOnlyPrecondition explains why SSO-only cannot be enabled or kept.
var ErrSSOOnlyPrecondition = users.Errf(users.KindPrecondition,
	"password login can be disabled only after an administrator completed an SSO test login on an enabled provider, and while at least one administrator can sign in with SSO")

// ssoOnlyReady: an enabled provider with a recorded test login and an enabled administrator with a linked identity on
// an enabled provider.
func ssoOnlyReady(ctx context.Context, q users.Querier) (bool, error) {
	ps, err := listProviders(ctx, q)
	if err != nil {
		return false, err
	}
	tested := false
	for _, p := range ps {
		if p.Enabled && p.TestLoginOKAt != nil {
			tested = true
		}
	}
	if !tested {
		return false, nil
	}
	n, err := users.CountSSOAdmins(ctx, q)
	return n > 0, err
}

// SetLocalLoginDisabled turns SSO-only enforcement on or off (recent authentication required by the API layer).
func (svc *Service) SetLocalLoginDisabled(ctx context.Context, actor audit.Actor, disabled bool) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		if disabled {
			ok, err := ssoOnlyReady(ctx, tx)
			if err != nil {
				return err
			}
			if !ok {
				return ErrSSOOnlyPrecondition
			}
		}
		if err := svc.auth.SetLocalLoginDisabledTx(ctx, tx, disabled, actor.Name); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "auth.local_login.set", "", audit.OK, map[string]any{"disabled": disabled})
	})
}

// checkSSOOnlyStillSafe refuses changes that would leave SSO-only on without a working SSO administrator.
func (svc *Service) checkSSOOnlyStillSafe(ctx context.Context, tx *sql.Tx) error {
	v, err := users.GetSetting(ctx, tx, users.SettingLocalLoginDisabled)
	if err != nil || v != "true" {
		return err
	}
	ok, err := ssoOnlyReady(ctx, tx)
	if err != nil {
		return err
	}
	if !ok {
		return users.Errf(users.KindPrecondition, "this change would leave password login disabled without an SSO administrator; re-enable password login first")
	}
	return nil
}

// LogoutURL returns the provider's RP-initiated logout URL when enabled, else "". It ends the session at that
// provider only; it is not a global logout.
func (svc *Service) LogoutURL(ctx context.Context, providerSlug, postLogoutRedirect string) (string, error) {
	p, err := svc.providerBySlug(ctx, providerSlug)
	if err != nil || !p.EndSessionEnabled || !p.Enabled {
		return "", err
	}
	r, err := svc.discover(ctx, p)
	if err != nil || r.endSession == "" {
		return "", err
	}
	u, err := url.Parse(r.endSession)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", p.ClientID)
	if postLogoutRedirect != "" {
		q.Set("post_logout_redirect_uri", svc.opts.BaseURL+SanitizeReturnTo(postLogoutRedirect))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
