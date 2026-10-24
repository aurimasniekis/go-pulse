// Package oidc is single sign-on for dashboard users (ADR-0038, PL-USR-008..011), ported from hallpass: provider
// configuration, Authorization Code flow with PKCE, ID-token validation, identity linking, just-in-time provisioning
// and group→role mapping, SSO-only enforcement.
package oidc

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// Provisioning modes.
const (
	Preprovisioned = "preprovisioned"
	JIT            = "jit"
)

// Step-up fallbacks.
const (
	StepUpNone      = "none"
	StepUpLocalTOTP = "local_totp"
)

// Provider is a provider configuration. The client secret is write-only.
type Provider struct {
	ID                        string     `json:"id"`
	Slug                      string     `json:"slug"`
	DisplayName               string     `json:"display_name"`
	Issuer                    string     `json:"issuer"`
	ClientID                  string     `json:"client_id"`
	HasClientSecret           bool       `json:"has_client_secret"`
	Scopes                    []string   `json:"scopes"`
	GroupsClaim               string     `json:"groups_claim"`
	Provisioning              string     `json:"provisioning"`
	JIT                       JITRules   `json:"jit"`
	AllowPrivateNetwork       bool       `json:"allow_private_network"`
	AllowCrossOriginEndpoints bool       `json:"allow_cross_origin_endpoints"`
	CABundlePEM               string     `json:"ca_bundle_pem"`
	StepUpFallback            string     `json:"stepup_fallback"`
	ACRValues                 []string   `json:"acr_values"`
	EndSessionEnabled         bool       `json:"end_session_enabled"`
	Enabled                   bool       `json:"enabled"`
	TestLoginOKAt             *time.Time `json:"test_login_ok_at,omitempty"`
	TestLoginBy               string     `json:"test_login_by,omitempty"`
	Version                   int64      `json:"version"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`

	secretEnc []byte
}

// JITRules restrict just-in-time provisioning; at least one is required in JIT mode and every configured rule must
// match.
type JITRules struct {
	EmailDomains  []string `json:"email_domains" doc:"Require email_verified=true and an email in one of these domains"`
	Tenants       []string `json:"tenants" doc:"Match the tid claim (Microsoft Entra ID)"`
	HostedDomains []string `json:"hosted_domains" doc:"Match the hd claim (Google Workspace)"`
	Groups        []string `json:"groups" doc:"Require one of these values in the groups claim"`
}

func (j JITRules) empty() bool {
	return len(j.EmailDomains) == 0 && len(j.Tenants) == 0 && len(j.HostedDomains) == 0 && len(j.Groups) == 0
}

func (j JITRules) nonNil() JITRules {
	for _, s := range []*[]string{&j.EmailDomains, &j.Tenants, &j.HostedDomains, &j.Groups} {
		if *s == nil {
			*s = []string{}
		}
	}
	return j
}

// ProviderInput creates or updates a provider. Nil fields are left unchanged on update. ClientSecret: nil keeps the
// stored secret, "" removes it.
type ProviderInput struct {
	Version                   int64     `json:"version,omitempty" doc:"Required on update (optimistic concurrency)"`
	Slug                      *string   `json:"slug,omitempty"`
	DisplayName               *string   `json:"display_name,omitempty"`
	Issuer                    *string   `json:"issuer,omitempty"`
	ClientID                  *string   `json:"client_id,omitempty"`
	ClientSecret              *string   `json:"client_secret,omitempty"`
	Scopes                    *[]string `json:"scopes,omitempty"`
	GroupsClaim               *string   `json:"groups_claim,omitempty"`
	Provisioning              *string   `json:"provisioning,omitempty" enum:"preprovisioned,jit"`
	JIT                       *JITRules `json:"jit,omitempty"`
	AllowPrivateNetwork       *bool     `json:"allow_private_network,omitempty"`
	AllowCrossOriginEndpoints *bool     `json:"allow_cross_origin_endpoints,omitempty"`
	CABundlePEM               *string   `json:"ca_bundle_pem,omitempty"`
	StepUpFallback            *string   `json:"stepup_fallback,omitempty" enum:"none,local_totp"`
	ACRValues                 *[]string `json:"acr_values,omitempty"`
	EndSessionEnabled         *bool     `json:"end_session_enabled,omitempty"`
	Enabled                   *bool     `json:"enabled,omitempty"`
}

// Mapping maps one groups-claim value to a role.
type Mapping struct {
	ID         string    `json:"id"`
	ProviderID string    `json:"provider_id"`
	GroupValue string    `json:"group_value"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
}

// Identity is an external identity linked to a user.
type Identity struct {
	Issuer      string     `json:"issuer"`
	Subject     string     `json:"subject"`
	ProviderID  string     `json:"provider_id"`
	UserID      string     `json:"user_id"`
	LinkedVia   string     `json:"linked_via"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Options configure the service.
type Options struct {
	// BaseURL is the dashboard's external URL; the redirect URI is BaseURL + CallbackPath.
	BaseURL string
	// DevMode allows http:// issuers on localhost.
	DevMode bool
	// StateTTL bounds pending authorizations (default and maximum 10 minutes).
	StateTTL    time.Duration
	HTTPTimeout time.Duration
	// JWKSMinRefetch rate-limits JWKS refetches on unknown key IDs.
	JWKSMinRefetch time.Duration
	// Skew is the accepted clock skew for exp/iat/nbf/auth_time (at most 60 s).
	Skew   time.Duration
	Logger *slog.Logger
}

// CallbackPath is the single redirect URI path for all providers (the state identifies the provider).
const CallbackPath = "/auth/oidc/callback"

// Service manages providers and runs SSO flows.
type Service struct {
	db   *store.DB
	auth *users.Service
	keys *users.Keys
	opts Options
	log  *slog.Logger

	mu     sync.Mutex
	remote map[string]*remote // provider ID → discovered endpoints, client and key set (per version)
}

// New returns the service.
func New(auth *users.Service, o Options) *Service {
	if o.StateTTL <= 0 || o.StateTTL > 10*time.Minute {
		o.StateTTL = 10 * time.Minute
	}
	if o.HTTPTimeout <= 0 {
		o.HTTPTimeout = 10 * time.Second
	}
	if o.JWKSMinRefetch <= 0 {
		o.JWKSMinRefetch = 10 * time.Second
	}
	if o.Skew <= 0 || o.Skew > time.Minute {
		o.Skew = time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	return &Service{db: auth.DB(), auth: auth, keys: auth.Keys(), opts: o, log: o.Logger.With("component", "oidc"), remote: map[string]*remote{}}
}

// RedirectURI is the exact redirect URI registered with every provider.
func (svc *Service) RedirectURI() string { return svc.opts.BaseURL + CallbackPath }

// BaseURL is the dashboard origin SSO runs on.
func (svc *Service) BaseURL() string { return svc.opts.BaseURL }

func (svc *Service) now() time.Time { return svc.auth.Now() }

var slugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func secretAAD(id string) []byte { return []byte("pulse/oidc-client-secret/v1|" + id) }

func invalid(format string, a ...any) error { return users.Errf(users.KindInvalid, format, a...) }

var errProviderNotFound = users.Errf(users.KindNotFound, "provider not found")

const providerCols = `id, slug, display_name, issuer, client_id, client_secret_enc, scopes, groups_claim, provisioning, jit, allow_private_network,
	allow_cross_origin_endpoints, ca_bundle_pem, stepup_fallback, acr_values, end_session_enabled, enabled, COALESCE(test_login_ok_at, 0),
	test_login_by, version, created_at, updated_at`

func scanProvider(r interface{ Scan(...any) error }) (*Provider, error) {
	p := &Provider{}
	var scopes, jit, acr string
	var testAt, created, updated int64
	if err := r.Scan(&p.ID, &p.Slug, &p.DisplayName, &p.Issuer, &p.ClientID, &p.secretEnc, &scopes, &p.GroupsClaim, &p.Provisioning, &jit,
		&p.AllowPrivateNetwork, &p.AllowCrossOriginEndpoints, &p.CABundlePEM, &p.StepUpFallback, &acr, &p.EndSessionEnabled, &p.Enabled,
		&testAt, &p.TestLoginBy, &p.Version, &created, &updated); err != nil {
		return nil, err
	}
	p.HasClientSecret = len(p.secretEnc) > 0
	if err := json.Unmarshal([]byte(scopes), &p.Scopes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(jit), &p.JIT); err != nil {
		return nil, err
	}
	p.JIT = p.JIT.nonNil()
	p.ACRValues = strings.Fields(acr)
	if p.ACRValues == nil {
		p.ACRValues = []string{}
	}
	if testAt != 0 {
		t := store.FromMs(testAt)
		p.TestLoginOKAt = &t
	}
	p.CreatedAt, p.UpdatedAt = store.FromMs(created), store.FromMs(updated)
	return p, nil
}

func (svc *Service) providerWhere(ctx context.Context, q users.Querier, where string, arg any) (*Provider, error) {
	p, err := scanProvider(q.QueryRowContext(ctx, "SELECT "+providerCols+" FROM oidc_providers WHERE "+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errProviderNotFound
	}
	return p, err
}

// GetProvider returns a provider by ID.
func (svc *Service) GetProvider(ctx context.Context, id string) (*Provider, error) {
	return svc.providerWhere(ctx, svc.db.R(), "id = ?", id)
}

func (svc *Service) providerBySlug(ctx context.Context, slug string) (*Provider, error) {
	return svc.providerWhere(ctx, svc.db.R(), "slug = ?", slug)
}

// ListProviders returns all providers.
func (svc *Service) ListProviders(ctx context.Context) ([]*Provider, error) {
	return listProviders(ctx, svc.db.R())
}

func listProviders(ctx context.Context, q users.Querier) ([]*Provider, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+providerCols+" FROM oidc_providers ORDER BY display_name, slug")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PublicProvider is what the login page may show.
type PublicProvider struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// EnabledProviders lists enabled providers for the login page.
func (svc *Service) EnabledProviders(ctx context.Context) ([]PublicProvider, error) {
	ps, err := svc.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := []PublicProvider{}
	for _, p := range ps {
		if p.Enabled {
			out = append(out, PublicProvider{Slug: p.Slug, DisplayName: p.DisplayName})
		}
	}
	return out, nil
}

// validateIssuerURL enforces https (or http on localhost in dev mode) and no query, fragment or userinfo.
func validateIssuerURL(raw string, dev bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return invalid("issuer must be an absolute URL without credentials, query or fragment")
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && dev && isLocalhost(u.Hostname()):
		return nil
	}
	return invalid("issuer must use https (http is allowed only for localhost in the dev profile)")
}

func isLocalhost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func (svc *Service) validate(p *Provider) error {
	if !slugRE.MatchString(p.Slug) {
		return invalid("slug must match %s", slugRE)
	}
	if n := strings.TrimSpace(p.DisplayName); n == "" || len(n) > 64 {
		return invalid("display name is required (at most 64 bytes)")
	}
	if err := validateIssuerURL(p.Issuer, svc.opts.DevMode); err != nil {
		return err
	}
	if p.ClientID == "" || len(p.ClientID) > 256 {
		return invalid("client ID is required (at most 256 bytes)")
	}
	if !slices.Contains(p.Scopes, "openid") {
		return invalid("scopes must include openid")
	}
	for _, s := range p.Scopes {
		if s == "" || strings.ContainsAny(s, " \t\r\n\"") || len(s) > 64 {
			return invalid("invalid scope %q", s)
		}
	}
	if len(p.GroupsClaim) > 64 {
		return invalid("groups claim name is too long")
	}
	switch p.Provisioning {
	case Preprovisioned:
	case JIT:
		if p.JIT.empty() {
			return invalid("JIT provisioning needs at least one restriction (email domains, tenants, hosted domains or groups)")
		}
		if len(p.JIT.Groups) > 0 && p.GroupsClaim == "" {
			return invalid("JIT group restrictions need a groups claim")
		}
	default:
		return invalid("provisioning must be preprovisioned or jit")
	}
	if p.StepUpFallback != StepUpNone && p.StepUpFallback != StepUpLocalTOTP {
		return invalid("stepup_fallback must be none or local_totp")
	}
	for _, a := range p.ACRValues {
		if strings.ContainsAny(a, " \t\r\n") || len(a) > 128 {
			return invalid("invalid acr value")
		}
	}
	if p.CABundlePEM != "" {
		if _, err := parseCABundle(p.CABundlePEM); err != nil {
			return err
		}
	}
	return nil
}

func parseCABundle(pemData string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(pemData)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return nil, invalid("CA bundle may contain only CERTIFICATE blocks")
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, invalid("CA bundle: %v", err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, invalid("CA bundle must be PEM certificates")
	}
	return certs, nil
}

func apply(p *Provider, in ProviderInput) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&p.Slug, in.Slug)
	set(&p.DisplayName, in.DisplayName)
	set(&p.Issuer, in.Issuer)
	set(&p.ClientID, in.ClientID)
	set(&p.GroupsClaim, in.GroupsClaim)
	set(&p.Provisioning, in.Provisioning)
	set(&p.StepUpFallback, in.StepUpFallback)
	if in.CABundlePEM != nil {
		p.CABundlePEM = *in.CABundlePEM
	}
	if in.Scopes != nil {
		p.Scopes = slices.Clone(*in.Scopes)
	}
	if in.ACRValues != nil {
		p.ACRValues = slices.Clone(*in.ACRValues)
	}
	if in.JIT != nil {
		p.JIT = in.JIT.nonNil()
		for i, d := range p.JIT.EmailDomains {
			p.JIT.EmailDomains[i] = strings.ToLower(strings.TrimSpace(d))
		}
	}
	for dst, v := range map[*bool]*bool{&p.AllowPrivateNetwork: in.AllowPrivateNetwork, &p.AllowCrossOriginEndpoints: in.AllowCrossOriginEndpoints,
		&p.EndSessionEnabled: in.EndSessionEnabled, &p.Enabled: in.Enabled} {
		if v != nil {
			*dst = *v
		}
	}
}

// CreateProvider adds a provider (recent authentication required by the API layer).
func (svc *Service) CreateProvider(ctx context.Context, actor audit.Actor, in ProviderInput) (*Provider, error) {
	p := &Provider{ID: users.NewID(), Scopes: []string{"openid", "profile", "email"}, Provisioning: Preprovisioned, StepUpFallback: StepUpNone,
		JIT: JITRules{}.nonNil()}
	apply(p, in)
	if err := svc.validate(p); err != nil {
		return nil, err
	}
	enc, err := svc.sealSecret(p.ID, in.ClientSecret)
	if err != nil {
		return nil, err
	}
	scopes, _ := json.Marshal(p.Scopes)
	jit, _ := json.Marshal(p.JIT)
	now := store.Ms(svc.now())
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := svc.providerWhere(ctx, tx, "slug = ?", p.Slug); err == nil {
			return users.Errf(users.KindConflict, "a provider with this slug exists")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO oidc_providers (id, slug, display_name, issuer, client_id, client_secret_enc, scopes, groups_claim,
			provisioning, jit, allow_private_network, allow_cross_origin_endpoints, ca_bundle_pem, stepup_fallback, acr_values, end_session_enabled,
			enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Slug, p.DisplayName, p.Issuer, p.ClientID, enc, string(scopes), p.GroupsClaim, p.Provisioning, string(jit), p.AllowPrivateNetwork,
			p.AllowCrossOriginEndpoints, p.CABundlePEM, p.StepUpFallback, strings.Join(p.ACRValues, " "), p.EndSessionEnabled, p.Enabled, now, now); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "oidc.provider.create", "oidc_provider:"+p.ID, audit.OK,
			providerAudit(p, in.ClientSecret != nil && *in.ClientSecret != ""))
	})
	if err != nil {
		return nil, err
	}
	return svc.GetProvider(ctx, p.ID)
}

func providerAudit(p *Provider, secretSet bool) map[string]any {
	return map[string]any{"slug": p.Slug, "issuer": p.Issuer, "client_id": p.ClientID, "provisioning": p.Provisioning, "enabled": p.Enabled,
		"allow_private_network": p.AllowPrivateNetwork, "allow_cross_origin_endpoints": p.AllowCrossOriginEndpoints, "client_secret_changed": secretSet}
}

func (svc *Service) sealSecret(id string, secret *string) ([]byte, error) {
	if secret == nil || *secret == "" {
		return nil, nil
	}
	if len(*secret) > 1024 {
		return nil, invalid("client secret is too long")
	}
	return svc.keys.Seal(secretAAD(id), []byte(*secret))
}

func (svc *Service) clientSecret(p *Provider) (string, error) {
	if len(p.secretEnc) == 0 {
		return "", nil
	}
	b, err := svc.keys.Open(secretAAD(p.ID), p.secretEnc)
	return string(b), err
}

// UpdateProvider changes a provider (optimistic concurrency by Version). Changing the issuer or client resets the
// test-login evidence.
func (svc *Service) UpdateProvider(ctx context.Context, actor audit.Actor, id string, in ProviderInput) (*Provider, error) {
	p, err := svc.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	prevIssuer, prevClient := p.Issuer, p.ClientID
	apply(p, in)
	if err := svc.validate(p); err != nil {
		return nil, err
	}
	enc := p.secretEnc
	if in.ClientSecret != nil {
		if enc, err = svc.sealSecret(p.ID, in.ClientSecret); err != nil {
			return nil, err
		}
	}
	testAt, testBy := sql.NullInt64{}, ""
	if p.TestLoginOKAt != nil && p.Issuer == prevIssuer && p.ClientID == prevClient {
		testAt, testBy = sql.NullInt64{Int64: store.Ms(*p.TestLoginOKAt), Valid: true}, p.TestLoginBy
	}
	scopes, _ := json.Marshal(p.Scopes)
	jit, _ := json.Marshal(p.JIT)
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if other, err := svc.providerWhere(ctx, tx, "slug = ?", p.Slug); err == nil && other.ID != p.ID {
			return users.Errf(users.KindConflict, "a provider with this slug exists")
		}
		res, err := tx.ExecContext(ctx, `UPDATE oidc_providers SET slug = ?, display_name = ?, issuer = ?, client_id = ?, client_secret_enc = ?, scopes = ?,
			groups_claim = ?, provisioning = ?, jit = ?, allow_private_network = ?, allow_cross_origin_endpoints = ?, ca_bundle_pem = ?, stepup_fallback = ?,
			acr_values = ?, end_session_enabled = ?, enabled = ?, test_login_ok_at = ?, test_login_by = ?, updated_at = ?, version = version + 1
			WHERE id = ? AND version = ?`,
			p.Slug, p.DisplayName, p.Issuer, p.ClientID, enc, string(scopes), p.GroupsClaim, p.Provisioning, string(jit), p.AllowPrivateNetwork,
			p.AllowCrossOriginEndpoints, p.CABundlePEM, p.StepUpFallback, strings.Join(p.ACRValues, " "), p.EndSessionEnabled, p.Enabled, testAt, testBy,
			store.Ms(svc.now()), p.ID, in.Version)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return users.Errf(users.KindConflict, "provider was modified concurrently; reload and retry")
		}
		if !p.Enabled {
			if err := svc.checkSSOOnlyStillSafe(ctx, tx); err != nil {
				return err
			}
		}
		return audit.Insert(ctx, tx, actor, "oidc.provider.update", "oidc_provider:"+p.ID, audit.OK, providerAudit(p, in.ClientSecret != nil))
	})
	if err != nil {
		return nil, err
	}
	svc.forget(p.ID)
	return svc.GetProvider(ctx, p.ID)
}

// DeleteProvider removes a provider with its mappings and linked identities. Users linked to it lose their
// SSO-sourced roles. Leaving no enabled administrator is refused.
func (svc *Service) DeleteProvider(ctx context.Context, actor audit.Actor, id string) error {
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		affected, err := linkedUsers(ctx, tx, id)
		if err != nil {
			return err
		}
		admins, err := users.NewAdminGuard(ctx, tx)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM oidc_providers WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errProviderNotFound
		}
		for _, uid := range affected {
			if err := users.ReplaceSSORolesTx(ctx, tx, uid, nil, nil); err != nil {
				return err
			}
			if err := svc.auth.RevokeSSOSessionsTx(ctx, tx, uid, "provider_deleted"); err != nil {
				return err
			}
		}
		if err := admins.Check(ctx, tx); err != nil {
			return err
		}
		if err := svc.checkSSOOnlyStillSafe(ctx, tx); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "oidc.provider.delete", "oidc_provider:"+id, audit.OK, map[string]any{"unlinked_users": len(affected)})
	})
	if err == nil {
		svc.forget(id)
	}
	return err
}

func linkedUsers(ctx context.Context, q users.Querier, providerID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT user_id FROM user_identities WHERE provider_id = ?", providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AddMapping adds a group→role mapping.
func (svc *Service) AddMapping(ctx context.Context, actor audit.Actor, providerID, groupValue, role string) (*Mapping, error) {
	groupValue = strings.TrimSpace(groupValue)
	if groupValue == "" || len(groupValue) > 256 {
		return nil, invalid("group value is required (at most 256 bytes)")
	}
	if !slices.Contains(users.AllRoles, role) {
		return nil, invalid("unknown role %q", role)
	}
	m := &Mapping{ID: users.NewID(), ProviderID: providerID, GroupValue: groupValue, Role: role, CreatedAt: svc.now()}
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := svc.providerWhere(ctx, tx, "id = ?", providerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO oidc_mappings (id, provider_id, group_value, role, created_at) VALUES (?, ?, ?, ?, ?)",
			m.ID, providerID, groupValue, role, store.Ms(m.CreatedAt)); err != nil {
			return users.Errf(users.KindConflict, "this mapping already exists")
		}
		return audit.Insert(ctx, tx, actor, "oidc.mapping.add", "oidc_provider:"+providerID, audit.OK,
			map[string]any{"group_value": groupValue, "role": role})
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// DeleteMapping removes a mapping; affected users lose the role at their next SSO login.
func (svc *Service) DeleteMapping(ctx context.Context, actor audit.Actor, providerID, id string) error {
	return svc.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM oidc_mappings WHERE id = ? AND provider_id = ?", id, providerID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return users.Errf(users.KindNotFound, "mapping not found")
		}
		return audit.Insert(ctx, tx, actor, "oidc.mapping.delete", "oidc_provider:"+providerID, audit.OK, map[string]any{"mapping_id": id})
	})
}

// ListMappings returns a provider's mappings.
func (svc *Service) ListMappings(ctx context.Context, providerID string) ([]Mapping, error) {
	rows, err := svc.db.R().QueryContext(ctx, "SELECT id, provider_id, group_value, role, created_at FROM oidc_mappings WHERE provider_id = ? ORDER BY group_value, role",
		providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mapping{}
	for rows.Next() {
		var m Mapping
		var c int64
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.GroupValue, &m.Role, &c); err != nil {
			return nil, err
		}
		m.CreatedAt = store.FromMs(c)
		out = append(out, m)
	}
	return out, rows.Err()
}
