// Package users is dashboard authentication (ADR-0038, PL-USR-*), ported from hallpass's operator auth: local
// accounts with Argon2id passwords, TOTP MFA and recovery codes, invitations and password resets, server-side web
// sessions with CSRF tokens, recent-authentication (step-up) checks, login throttling, personal access tokens for the
// CLI, and the setup and break-glass one-time tokens issued by `pulsed admin`. Single sign-on lives in internal/oidc.
package users

import (
	"errors"
	"fmt"
)

// Kind classifies an error for the HTTP layer. Messages are safe to show to the user.
type Kind string

// Error kinds.
const (
	KindInvalid      Kind = "invalid"
	KindUnauthorized Kind = "unauthorized"
	KindForbidden    Kind = "forbidden"
	KindNotFound     Kind = "not_found"
	KindConflict     Kind = "conflict"
	KindPrecondition Kind = "precondition"
	KindRateLimited  Kind = "rate_limited"
	KindReauth       Kind = "reauth_required"
	KindMFARequired  Kind = "mfa_required"
	KindUnavailable  Kind = "unavailable"
)

// Error is a user-facing error with a stable kind.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Is matches errors of the same kind and message (sentinels compare by value).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind && t.Msg == e.Msg
}

// Errf returns a new Error.
func Errf(k Kind, format string, a ...any) *Error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, a...)}
}

// KindOf returns the kind of err, or "" for internal errors.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// Shared errors. Every failed login gets the same message (PL-USR-005).
var (
	ErrInvalidCredentials = Errf(KindUnauthorized, "invalid credentials")
	ErrUnauthorized       = Errf(KindUnauthorized, "authentication required")
	ErrThrottled          = Errf(KindRateLimited, "too many attempts; try again later")
	ErrReauthRequired     = Errf(KindReauth, "recent authentication required")
	ErrMFARequired        = Errf(KindMFARequired, "multi-factor authentication required")
	ErrTokenInvalid       = Errf(KindUnauthorized, "this link or token is invalid or has expired")
	ErrLastAdmin          = Errf(KindPrecondition, "this change would leave no enabled administrator")
	ErrLastSSOAdmin       = Errf(KindPrecondition,
		"password login is disabled and this change would leave no enabled administrator who can sign in with SSO; re-enable password login first")
	ErrLocalLoginDisabled = Errf(KindPrecondition, "password login is disabled; sign in with SSO")
	ErrNoMFAPending       = Errf(KindPrecondition, "this session is not waiting for a second factor")
	ErrCompleteMFAFirst   = Errf(KindForbidden, "verify your current second factor before changing it")
	ErrNotFound           = Errf(KindNotFound, "user not found")
)
