// Package rbac is the dashboard permission matrix (ADR-0038, PL-USR-003). Every admin API operation names exactly
// one permission (Huma operation metadata); Authorize is the single server-side decision point and depends only on
// the principal's roles, never on how it authenticated.
package rbac

import (
	"slices"

	"github.com/aurimasniekis/go-pulse/internal/users"
)

// Permission is a named capability.
type Permission string

// Permissions.
const (
	DashboardRead  Permission = "dashboard.read"  // overview, rollups, subjects, streams and consumers (metadata)
	EventsRead     Permission = "events.read"     // event payloads: explorer, tail, KV values, object downloads
	EventsPublish  Permission = "events.publish"  // publish an event as the operator
	StreamsWrite   Permission = "streams.write"   // create/update/delete streams, consumers, KV and object buckets and entries
	ClientsRead    Permission = "clients.read"    // clients, grants, connections, HTTP tokens
	ClientsWrite   Permission = "clients.write"   // create/rotate/revoke/kick clients; HTTP tokens
	WebhooksRead   Permission = "webhooks.read"   // inbound/outbound hooks and the delivery log
	WebhooksWrite  Permission = "webhooks.write"  // create/update/delete hooks, rotate secrets, retry deliveries
	SecurityRead   Permission = "security.read"   // bans, failures, allowlist
	SecurityWrite  Permission = "security.write"  // ban, lift, allowlist
	AlertsRead     Permission = "alerts.read"     // rules, instances, history, notifiers
	AlertsWrite    Permission = "alerts.write"    // rule changes, evaluate, notifier tests
	BackupsRead    Permission = "backups.read"    // catalog and status
	BackupsRun     Permission = "backups.run"     // run and prune
	BackupsRestore Permission = "backups.restore" // dry runs and restores (they take the age identity)
	AuditRead      Permission = "audit.read"      // the audit log
	SystemRead     Permission = "system.read"     // server, NATS, certificates, connections, configuration
	UsersRead      Permission = "users.read"      // users, sessions, identities, invitations
	UsersManage    Permission = "users.manage"    // create/update/delete users, invitations, reset links, sessions, links
	AuthSettings   Permission = "auth.settings"   // SSO providers, mappings, test logins, SSO-only enforcement
)

// Pseudo-permissions for operations that are not role-gated.
const (
	// Public operations need no session (auth config, login, setup, invitations, resets, OIDC callback).
	Public Permission = "public"
	// Session operations need any full session (self-service: account, sessions, tokens).
	Session Permission = "session"
	// Limited operations are allowed while MFA is still pending (MFA verification and enrollment, logout, me).
	Limited Permission = "session.limited"
)

var (
	viewer   = []Permission{DashboardRead, EventsRead, ClientsRead, WebhooksRead, SecurityRead, AlertsRead, BackupsRead, SystemRead}
	auditor  = append(slices.Clone(viewer), AuditRead, UsersRead)
	operator = append(slices.Clone(viewer), AuditRead, EventsPublish, StreamsWrite, ClientsWrite, WebhooksWrite, SecurityWrite, AlertsWrite, BackupsRun)
	admin    = append(slices.Clone(operator), BackupsRestore, UsersRead, UsersManage, AuthSettings)
	matrix   = map[string][]Permission{users.RoleAdministrator: admin, users.RoleOperator: operator, users.RoleAuditor: auditor, users.RoleViewer: viewer}
)

// Has reports whether any of the roles holds the permission.
func Has(roles []string, p Permission) bool {
	for _, r := range roles {
		if slices.Contains(matrix[r], p) {
			return true
		}
	}
	return false
}

// Permissions returns the union of permissions for roles, sorted (for /me and the UI).
func Permissions(roles []string) []Permission {
	out := []Permission{}
	for _, r := range roles {
		for _, p := range matrix[r] {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}

// ErrForbidden is the uniform denial.
var ErrForbidden = users.Errf(users.KindForbidden, "you do not have permission for this action")

// Authorize is the single server-side check. Users without roles hold nothing.
func Authorize(p *users.Principal, perm Permission) error {
	switch perm {
	case Public:
		return nil
	case Limited:
		if p == nil {
			return users.ErrUnauthorized
		}
		return nil
	}
	if p == nil {
		return users.ErrUnauthorized
	}
	if p.Limited {
		return users.ErrMFARequired
	}
	if perm == Session {
		return nil
	}
	if !Has(p.User.Roles, perm) {
		return ErrForbidden
	}
	return nil
}

// Known reports whether perm is defined (used by the operation conformance test).
func Known(perm Permission) bool {
	switch perm {
	case Public, Session, Limited:
		return true
	}
	return slices.Contains(admin, perm)
}
