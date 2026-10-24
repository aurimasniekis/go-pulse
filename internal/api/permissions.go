package api

import "github.com/aurimasniekis/go-pulse/internal/rbac"

// opRule is the authorization rule of one admin operation (ADR-0038). Every operation registered on the admin API
// must have a rule (TestEveryOperationHasARule); the Huma middleware in authn.go enforces it.
type opRule struct {
	perm rbac.Permission
	// reauth requires authentication within the reauth window (step-up). mfa additionally requires the second factor
	// within the window for password sessions.
	reauth, mfa bool
}

func rule(p rbac.Permission) opRule          { return opRule{perm: p} }
func reauth(p rbac.Permission) opRule        { return opRule{perm: p, reauth: true} }
func reauthWithMFA(p rbac.Permission) opRule { return opRule{perm: p, reauth: true, mfa: true} }

var opRules = map[string]opRule{
	// system and dashboards
	"get-system-info":      rule(rbac.DashboardRead),
	"get-config":           rule(rbac.SystemRead),
	"get-certificates":     rule(rbac.SystemRead),
	"list-connections":     rule(rbac.SystemRead),
	"get-overview":         rule(rbac.DashboardRead),
	"get-throughput":       rule(rbac.DashboardRead),
	"get-subject-tree":     rule(rbac.DashboardRead),
	"get-subject":          rule(rbac.DashboardRead),
	"list-streams":         rule(rbac.DashboardRead),
	"get-stream":           rule(rbac.DashboardRead),
	"list-consumers":       rule(rbac.DashboardRead),
	"get-consumer":         rule(rbac.DashboardRead),
	"put-consumer":         rule(rbac.StreamsWrite),
	"create-stream":        rule(rbac.StreamsWrite),
	"update-stream":        rule(rbac.StreamsWrite),
	"delete-stream":        reauth(rbac.StreamsWrite),
	"delete-consumer":      rule(rbac.StreamsWrite),
	"query-events":         rule(rbac.EventsRead),
	"get-event":            rule(rbac.EventsRead),
	"tail-events":          rule(rbac.EventsRead),
	"publish-event":        rule(rbac.EventsPublish),
	"list-buckets":         rule(rbac.DashboardRead),
	"list-kv-keys":         rule(rbac.DashboardRead),
	"get-kv-entry":         rule(rbac.EventsRead),
	"create-kv-bucket":     rule(rbac.StreamsWrite),
	"get-kv-bucket":        rule(rbac.DashboardRead),
	"update-kv-bucket":     rule(rbac.StreamsWrite),
	"delete-kv-bucket":     reauth(rbac.StreamsWrite),
	"put-kv-entry":         rule(rbac.StreamsWrite),
	"delete-kv-entry":      rule(rbac.StreamsWrite),
	"list-objects":         rule(rbac.DashboardRead),
	"download-object":      rule(rbac.EventsRead),
	"create-object-bucket": rule(rbac.StreamsWrite),
	"get-object-bucket":    rule(rbac.DashboardRead),
	"update-object-bucket": rule(rbac.StreamsWrite),
	"delete-object-bucket": reauth(rbac.StreamsWrite),
	"upload-object":        rule(rbac.StreamsWrite),
	"delete-object":        rule(rbac.StreamsWrite),

	// clients and HTTP tokens (credentials are shown once: recent authentication)
	"list-clients":         rule(rbac.ClientsRead),
	"get-client":           rule(rbac.ClientsRead),
	"client-connections":   rule(rbac.ClientsRead),
	"create-client":        reauth(rbac.ClientsWrite),
	"update-client":        rule(rbac.ClientsWrite),
	"update-client-grants": reauth(rbac.ClientsWrite),
	"rotate-client":        reauth(rbac.ClientsWrite),
	"revoke-client":        rule(rbac.ClientsWrite),
	"delete-client":        rule(rbac.ClientsWrite),
	"kick-client":          rule(rbac.ClientsWrite),
	"list-tokens":          rule(rbac.ClientsRead),
	"create-token":         reauth(rbac.ClientsWrite),
	"revoke-token":         rule(rbac.ClientsWrite),
	"delete-token":         rule(rbac.ClientsWrite),

	// webhooks (secrets are shown on create and rotate)
	"list-inbound-hooks":   rule(rbac.WebhooksRead),
	"create-inbound-hook":  reauth(rbac.WebhooksWrite),
	"update-inbound-hook":  reauth(rbac.WebhooksWrite),
	"delete-inbound-hook":  rule(rbac.WebhooksWrite),
	"list-outbound-hooks":  rule(rbac.WebhooksRead),
	"get-outbound-hook":    rule(rbac.WebhooksRead),
	"create-outbound-hook": reauth(rbac.WebhooksWrite),
	"update-outbound-hook": reauth(rbac.WebhooksWrite),
	"delete-outbound-hook": rule(rbac.WebhooksWrite),
	"pause-outbound-hook":  rule(rbac.WebhooksWrite),
	"resume-outbound-hook": rule(rbac.WebhooksWrite),
	"test-outbound-hook":   rule(rbac.WebhooksWrite),
	"list-deliveries":      rule(rbac.WebhooksRead),
	"retry-delivery":       rule(rbac.WebhooksWrite),

	// security, alerts, audit
	"get-security":       rule(rbac.SecurityRead),
	"create-ban":         rule(rbac.SecurityWrite),
	"lift-ban":           rule(rbac.SecurityWrite),
	"add-allow":          rule(rbac.SecurityWrite),
	"remove-allow":       rule(rbac.SecurityWrite),
	"get-alerts":         rule(rbac.AlertsRead),
	"list-alert-history": rule(rbac.AlertsRead),
	"create-alert-rule":  rule(rbac.AlertsWrite),
	"update-alert-rule":  rule(rbac.AlertsWrite),
	"delete-alert-rule":  rule(rbac.AlertsWrite),
	"evaluate-alerts":    rule(rbac.AlertsWrite),
	"test-notifier":      rule(rbac.AlertsWrite),
	"list-audit":         rule(rbac.AuditRead),

	// backups (restores take the age identity and can replace streams)
	"get-backups":         rule(rbac.BackupsRead),
	"list-remote-backups": rule(rbac.BackupsRead),
	"run-backup":          rule(rbac.BackupsRun),
	"prune-backups":       rule(rbac.BackupsRun),
	"plan-restore":        reauthWithMFA(rbac.BackupsRestore),
	"restore-backup":      reauthWithMFA(rbac.BackupsRestore),

	// sign-in (public: the request carries its own proof)
	"get-auth-config":   rule(rbac.Public),
	"login":             rule(rbac.Public),
	"setup":             rule(rbac.Public),
	"accept-invitation": rule(rbac.Public),
	"reset-password":    rule(rbac.Public),
	"recover-login":     rule(rbac.Public),
	"verify-mfa":        rule(rbac.Limited),
	"logout":            rule(rbac.Limited),
	"get-me":            rule(rbac.Limited),
	"reauth":            rule(rbac.Session),
	"reauth-totp":       rule(rbac.Session),

	// own account
	"list-my-sessions":          rule(rbac.Session),
	"revoke-my-session":         rule(rbac.Session),
	"change-my-password":        rule(rbac.Session),
	"begin-totp":                rule(rbac.Limited), // enrollment gate in users.enrollGate
	"confirm-totp":              rule(rbac.Limited),
	"disable-totp":              rule(rbac.Session),
	"regenerate-recovery-codes": rule(rbac.Session),
	"list-my-identities":        rule(rbac.Session),
	"list-my-tokens":            rule(rbac.Session),
	"create-my-token":           reauthWithMFA(rbac.Session),
	"revoke-my-token":           rule(rbac.Session),

	// users and invitations
	"list-users":           rule(rbac.UsersRead),
	"get-user":             rule(rbac.UsersRead),
	"create-user":          reauthWithMFA(rbac.UsersManage),
	"update-user":          reauthWithMFA(rbac.UsersManage),
	"delete-user":          reauthWithMFA(rbac.UsersManage),
	"issue-reset-link":     reauthWithMFA(rbac.UsersManage),
	"list-user-sessions":   rule(rbac.UsersRead),
	"revoke-user-sessions": rule(rbac.UsersManage),
	"list-user-identities": rule(rbac.UsersRead),
	"link-user-identity":   reauthWithMFA(rbac.UsersManage),
	"unlink-user-identity": reauthWithMFA(rbac.UsersManage),
	"issue-link-invite":    reauthWithMFA(rbac.UsersManage),
	"clear-user-throttle":  rule(rbac.UsersManage),
	"list-user-tokens":     rule(rbac.UsersRead),
	"revoke-user-token":    rule(rbac.UsersManage),
	"list-invitations":     rule(rbac.UsersRead),
	"create-invitation":    reauthWithMFA(rbac.UsersManage),
	"revoke-invitation":    rule(rbac.UsersManage),

	// single sign-on
	"list-sso-providers":   rule(rbac.AuthSettings),
	"get-sso-provider":     rule(rbac.AuthSettings),
	"create-sso-provider":  reauthWithMFA(rbac.AuthSettings),
	"update-sso-provider":  reauthWithMFA(rbac.AuthSettings),
	"delete-sso-provider":  reauthWithMFA(rbac.AuthSettings),
	"list-sso-mappings":    rule(rbac.AuthSettings),
	"add-sso-mapping":      reauthWithMFA(rbac.AuthSettings),
	"delete-sso-mapping":   reauthWithMFA(rbac.AuthSettings),
	"get-sso-test-result":  rule(rbac.AuthSettings),
	"get-auth-settings":    rule(rbac.AuthSettings),
	"update-auth-settings": reauthWithMFA(rbac.AuthSettings),
}
