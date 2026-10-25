package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/rbac"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// Flow purposes.
const (
	PurposeLogin  = "login"
	PurposeStepUp = "stepup"
	PurposeTest   = "test"
	PurposeLink   = "link"
)

// Flow errors. Messages are safe to show; they never reveal whether an account exists beyond "not provisioned".
var (
	ErrInvalidState    = users.Errf(users.KindInvalid, "sign-in session expired or invalid; start again")
	ErrNotProvisioned  = users.Errf(users.KindForbidden, "this identity is not provisioned in Pulse; ask an administrator")
	ErrLoginRejected   = users.Errf(users.KindForbidden, "sign-in was rejected")
	ErrStepUpFailed    = users.Errf(users.KindForbidden, "the identity provider did not confirm a fresh login")
	ErrTokenValidation = users.Errf(users.KindUnauthorized, "the identity provider's response could not be validated")
)

// StartInput begins an authorization.
type StartInput struct {
	Provider  string // slug
	Purpose   string
	ReturnTo  string
	Principal *users.Principal // required for stepup, test and self-linking
	LinkToken string           // an admin-issued link invitation (purpose link without a session)
}

// StartResult tells the HTTP layer where to redirect and which binding cookie to set.
type StartResult struct {
	RedirectURL string
	Binding     string
	ExpiresAt   time.Time
}

func random32() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashOf(domain, v string) []byte {
	h := sha256.Sum256([]byte("pulse/oidc/" + domain + "/v1|" + v))
	return h[:]
}

// SanitizeReturnTo accepts only same-origin relative paths outside /api and /auth.
func SanitizeReturnTo(p string) string {
	if p == "" || len(p) > 512 || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	// Dot segments (also percent-encoded: u.Path is decoded) would let the browser resolve "/./api/…" past the deny
	// list below; no dashboard route needs them.
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return "/"
		}
	}
	if strings.HasPrefix(u.Path, "/api/") || strings.HasPrefix(u.Path, "/auth/") || u.Path == "/api" || u.Path == "/auth" {
		return "/"
	}
	// Redirect writers clean everything before "?" as a path, fragment included: "/#/../api/…" would become "/api/…".
	pre, _, _ := strings.Cut(p, "?")
	if cleaned := path.Clean(pre); cleaned != pre && cleaned+"/" != pre {
		return "/"
	}
	return p
}

func verifierAAD(stateHash []byte) []byte { return append([]byte("pulse/oidc-pkce/v1|"), stateHash...) }

// Start creates a single-use server-side state (state, nonce, PKCE verifier; 10 minutes) and returns the provider's
// authorization URL.
func (svc *Service) Start(ctx context.Context, in StartInput) (*StartResult, error) {
	p, err := svc.providerBySlug(ctx, in.Provider)
	if err != nil {
		return nil, err
	}
	if !p.Enabled && in.Purpose != PurposeTest {
		return nil, errProviderNotFound
	}
	var userID, sessionDisplay string
	switch in.Purpose {
	case PurposeLogin:
	case PurposeStepUp:
		if in.Principal == nil || in.Principal.Method != users.MethodOIDC {
			return nil, users.Errf(users.KindPrecondition, "step-up through SSO needs an SSO session")
		}
		userID, sessionDisplay = in.Principal.User.ID, in.Principal.SessionID
	case PurposeTest:
		if !canTest(in.Principal) {
			return nil, users.Errf(users.KindForbidden, "only administrators can run an SSO test login")
		}
		userID, sessionDisplay = in.Principal.User.ID, in.Principal.SessionID
	case PurposeLink:
		switch {
		case in.LinkToken != "":
			if userID, err = svc.checkLinkToken(ctx, in.LinkToken, p.ID); err != nil {
				return nil, err
			}
		case in.Principal != nil && !in.Principal.Limited && in.Principal.Method != users.MethodToken:
			// Linking needs recent authentication, with MFA where policy requires it or a factor is enrolled.
			if err := svc.auth.RequireRecentAuth(in.Principal, in.Principal.User.MFARequired); err != nil {
				return nil, err
			}
			userID, sessionDisplay = in.Principal.User.ID, in.Principal.SessionID
		default:
			return nil, users.Errf(users.KindUnauthorized, "linking needs a signed-in session or a link invitation")
		}
	default:
		return nil, invalid("unknown purpose")
	}
	r, err := svc.discover(ctx, p)
	if err != nil {
		return nil, err
	}
	state, err := random32()
	if err != nil {
		return nil, err
	}
	nonce, err := random32()
	if err != nil {
		return nil, err
	}
	binding, err := random32()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	sh := hashOf("state", state)
	venc, err := svc.keys.Seal(verifierAAD(sh), []byte(verifier))
	if err != nil {
		return nil, err
	}
	now := svc.now()
	exp := now.Add(svc.opts.StateTTL)
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO oidc_auth_states (state_hash, provider_id, purpose, nonce_hash, verifier_enc, binding_hash, return_to,
			user_id, session_display, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sh, p.ID, in.Purpose, hashOf("nonce", nonce), venc, hashOf("binding", binding), SanitizeReturnTo(in.ReturnTo),
			sql.NullString{String: userID, Valid: userID != ""}, sql.NullString{String: sessionDisplay, Valid: sessionDisplay != ""},
			store.Ms(now), store.Ms(exp))
		return err
	})
	if err != nil {
		return nil, err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), gooidc.Nonce(nonce)}
	if in.Purpose != PurposeLogin {
		// Step-up, test and link require a fresh login at the provider.
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	if len(p.ACRValues) > 0 {
		opts = append(opts, oauth2.SetAuthURLParam("acr_values", strings.Join(p.ACRValues, " ")))
	}
	return &StartResult{RedirectURL: svc.oauthConfig(p, r, "").AuthCodeURL(state, opts...), Binding: binding, ExpiresAt: exp}, nil
}

// canTest reports whether a session may run an SSO test login: a full administrator session.
func canTest(p *users.Principal) bool {
	return p != nil && !p.Limited && p.Method != users.MethodToken && rbac.Has(p.User.Roles, rbac.AuthSettings)
}

func (svc *Service) oauthConfig(p *Provider, r *remote, secret string) *oauth2.Config {
	return &oauth2.Config{ClientID: p.ClientID, ClientSecret: secret, Endpoint: r.provider.Endpoint(), RedirectURL: svc.RedirectURI(), Scopes: p.Scopes}
}

// CallbackInput is the provider's redirect back to Pulse.
type CallbackInput struct {
	State, Code         string
	Error, ErrorDetails string
	Binding             string // the binding cookie
	Principal           *users.Principal
	Meta                users.SessionMeta
}

// CallbackResult is the outcome per purpose.
type CallbackResult struct {
	Purpose      string
	ProviderID   string
	ReturnTo     string
	SessionToken string // login and stepup
	UserID       string
	Test         *TestResult
}

// Claims are the ID-token claims Pulse uses.
type Claims struct {
	Issuer        string   `json:"iss"`
	Subject       string   `json:"sub"`
	Email         string   `json:"email,omitempty"`
	EmailVerified bool     `json:"email_verified"`
	Name          string   `json:"name,omitempty"`
	Groups        []string `json:"groups,omitempty"`
	Tenant        string   `json:"tid,omitempty"`
	HostedDomain  string   `json:"hd,omitempty"`
	AuthTime      *int64   `json:"auth_time,omitempty"`
	ACR           string   `json:"acr,omitempty"`
}

// TestResult is the claim-mapping preview of a test login. No session is created.
type TestResult struct {
	Claims          Claims   `json:"claims"`
	LinkedUserID    string   `json:"linked_user_id,omitempty"`
	WouldProvision  bool     `json:"would_provision"`
	ProvisionReason string   `json:"provision_reason,omitempty"`
	MappedRoles     []string `json:"mapped_roles"`
	EffectiveRoles  []string `json:"effective_roles"`
}

// Callback validates the response and completes the purpose. The state is consumed first (single use).
func (svc *Service) Callback(ctx context.Context, in CallbackInput) (_ *CallbackResult, cbErr error) {
	if in.State == "" || len(in.State) > 128 {
		return nil, ErrInvalidState
	}
	var (
		providerID, purpose, returnTo string
		nonceHash, venc, bindingHash  []byte
		userID, sessionDisplay        sql.NullString
		created, expires              int64
	)
	sh := hashOf("state", in.State)
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `DELETE FROM oidc_auth_states WHERE state_hash = ? RETURNING provider_id, purpose, nonce_hash, verifier_enc,
			binding_hash, return_to, user_id, session_display, created_at, expires_at`, sh).
			Scan(&providerID, &purpose, &nonceHash, &venc, &bindingHash, &returnTo, &userID, &sessionDisplay, &created, &expires)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	if svc.now().After(store.FromMs(expires)) || subtle.ConstantTimeCompare(bindingHash, hashOf("binding", in.Binding)) != 1 {
		return nil, ErrInvalidState
	}
	if in.Error != "" {
		svc.log.Info("identity provider returned an error", "error", truncate(in.Error, 64))
		return nil, ErrLoginRejected
	}
	p, err := svc.GetProvider(ctx, providerID)
	if err != nil {
		return nil, ErrInvalidState
	}
	if purpose == PurposeLogin {
		defer func() { svc.auth.Observe(users.MethodOIDC, cbErr) }()
	}
	if !p.Enabled && purpose != PurposeTest {
		return nil, ErrLoginRejected
	}
	r, err := svc.discover(ctx, p)
	if err != nil {
		return nil, err
	}
	verifier, err := svc.keys.Open(verifierAAD(sh), venc)
	if err != nil {
		return nil, ErrInvalidState
	}
	secret, err := svc.clientSecret(p)
	if err != nil {
		return nil, err
	}
	tok, err := svc.oauthConfig(p, r, secret).Exchange(gooidc.ClientContext(ctx, r.client), in.Code, oauth2.VerifierOption(string(verifier)))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode < 500 {
			svc.log.Warn("oidc code exchange rejected", "provider", p.Slug, "status", re.Response.StatusCode)
			return nil, ErrLoginRejected
		}
		svc.log.Warn("oidc token endpoint unavailable", "provider", p.Slug, "err", err.Error())
		return nil, ErrProviderUnavailable
	}
	raw, _ := tok.Extra("id_token").(string)
	// Access and refresh tokens are dropped here: Pulse has no use for them.
	claims, err := svc.verifyIDToken(ctx, p, r, raw, nonceHash)
	if err != nil {
		svc.log.Warn("id token rejected", "provider", p.Slug, "err", err.Error())
		return nil, ErrTokenValidation
	}
	st := authState{providerID: providerID, userID: userID.String, sessionDisplay: sessionDisplay.String, hasSession: sessionDisplay.Valid,
		created: store.FromMs(created)}
	res := &CallbackResult{Purpose: purpose, ReturnTo: returnTo, ProviderID: p.ID}
	switch purpose {
	case PurposeLogin:
		res.SessionToken, res.UserID, err = svc.login(ctx, p, claims, in.Meta)
	case PurposeStepUp:
		res.SessionToken, err = svc.stepUp(ctx, p, st, claims, in)
		res.UserID = st.userID
	case PurposeTest:
		res.Test, err = svc.testLogin(ctx, p, st, claims, in.Principal)
	case PurposeLink:
		res.UserID, err = svc.link(ctx, p, st, claims, in)
	default:
		err = ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

type authState struct {
	providerID, userID, sessionDisplay string
	hasSession                         bool
	created                            time.Time
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// verifyIDToken checks the signature (RS256/ES256/EdDSA via JWKS), iss, aud, azp, exp/iat/nbf with ≤ 60 s skew and
// the nonce.
func (svc *Service) verifyIDToken(ctx context.Context, p *Provider, r *remote, raw string, nonceHash []byte) (*Claims, error) {
	if raw == "" {
		return nil, errors.New("token response has no id_token")
	}
	now := svc.now()
	v := gooidc.NewVerifier(p.Issuer, r.keys, &gooidc.Config{ClientID: p.ClientID, SupportedSigningAlgs: signingAlgs,
		Now: func() time.Time { return now.Add(-svc.opts.Skew) }})
	t, err := v.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	var extra struct {
		AZP string `json:"azp"`
		IAT int64  `json:"iat"`
		NBF *int64 `json:"nbf"`
	}
	if err := t.Claims(&extra); err != nil {
		return nil, err
	}
	if (len(t.Audience) > 1 || extra.AZP != "") && extra.AZP != p.ClientID {
		return nil, errors.New("azp does not match the client ID")
	}
	if extra.IAT == 0 || time.Unix(extra.IAT, 0).After(now.Add(svc.opts.Skew)) {
		return nil, errors.New("iat missing or in the future")
	}
	if extra.NBF != nil && time.Unix(*extra.NBF, 0).After(now.Add(svc.opts.Skew)) {
		return nil, errors.New("token not yet valid")
	}
	if subtle.ConstantTimeCompare(hashOf("nonce", t.Nonce), nonceHash) != 1 {
		return nil, errors.New("nonce mismatch")
	}
	var c Claims
	if err := t.Claims(&c); err != nil {
		return nil, err
	}
	c.Groups = nil
	if p.GroupsClaim != "" {
		var all map[string]json.RawMessage
		if err := t.Claims(&all); err == nil {
			c.Groups = parseGroups(claimAt(all, p.GroupsClaim))
		}
	}
	if c.Subject == "" || len(c.Subject) > 255 {
		return nil, errors.New("sub missing or too long")
	}
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	return &c, nil
}

// claimAt returns the claim named path. A top-level claim with exactly that name wins, so names containing dots
// (`https://example.com/groups`) keep working; otherwise a dotted path walks nested objects (`porch.groups`).
func claimAt(all map[string]json.RawMessage, path string) json.RawMessage {
	if raw, ok := all[path]; ok {
		return raw
	}
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil
	}
	cur := all
	for _, part := range parts[:len(parts)-1] {
		var next map[string]json.RawMessage
		if json.Unmarshal(cur[part], &next) != nil {
			return nil
		}
		cur = next
	}
	return cur[parts[len(parts)-1]]
}

// parseGroups accepts a string array or a single string; anything else yields no groups.
func parseGroups(raw json.RawMessage) []string {
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		if len(arr) > 1000 {
			arr = arr[:1000]
		}
		return arr
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	return nil
}

// mapped evaluates the allowlisted mappings for the claims' groups.
func (svc *Service) mapped(ctx context.Context, p *Provider, groups []string) ([]string, map[string]string, error) {
	ms, err := svc.ListMappings(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	roles, refs := []string{}, map[string]string{}
	for _, m := range ms {
		if slices.Contains(groups, m.GroupValue) && !slices.Contains(roles, m.Role) {
			roles = append(roles, m.Role)
			refs[m.Role] = m.GroupValue
		}
	}
	slices.Sort(roles)
	return roles, refs, nil
}

// jitAllowed checks every configured JIT restriction.
func jitAllowed(rules JITRules, c *Claims) (bool, string) {
	if rules.empty() {
		return false, "no JIT restrictions configured"
	}
	if len(rules.EmailDomains) > 0 {
		at := strings.LastIndexByte(c.Email, '@')
		if !c.EmailVerified || at < 0 || !slices.Contains(rules.EmailDomains, c.Email[at+1:]) {
			return false, "verified email domain not allowed"
		}
	}
	if len(rules.Tenants) > 0 && !slices.Contains(rules.Tenants, c.Tenant) {
		return false, "tenant not allowed"
	}
	if len(rules.HostedDomains) > 0 && !slices.Contains(rules.HostedDomains, c.HostedDomain) {
		return false, "hosted domain not allowed"
	}
	if len(rules.Groups) > 0 && !slices.ContainsFunc(c.Groups, func(g string) bool { return slices.Contains(rules.Groups, g) }) {
		return false, "no allowed group"
	}
	return true, ""
}

type deniedError struct {
	reason string
	pub    error
}

func (e *deniedError) Error() string { return e.reason }

// login resolves (iss, sub) to a user — never by email — provisions JIT users, syncs SSO roles and creates the
// session, all in one transaction.
func (svc *Service) login(ctx context.Context, p *Provider, c *Claims, meta users.SessionMeta) (string, string, error) {
	roles, refs, err := svc.mapped(ctx, p, c.Groups)
	if err != nil {
		return "", "", err
	}
	var token, userID string
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?", c.Issuer, c.Subject).Scan(&userID)
		switch {
		case err == nil:
		case errors.Is(err, sql.ErrNoRows):
			if p.Provisioning != JIT {
				return &deniedError{reason: "not_provisioned", pub: ErrNotProvisioned}
			}
			if ok, why := jitAllowed(p.JIT, c); !ok {
				return &deniedError{reason: "jit_restricted: " + why, pub: ErrNotProvisioned}
			}
			if userID, err = svc.provisionTx(ctx, tx, p, c); err != nil {
				return err
			}
		default:
			return err
		}
		u, err := svc.auth.GetTx(ctx, tx, userID)
		if err != nil || !u.Enabled {
			return &deniedError{reason: "user_disabled", pub: ErrLoginRejected}
		}
		if err := svc.syncTx(ctx, tx, userID, roles, refs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE user_identities SET last_login_at = ? WHERE issuer = ? AND subject = ?",
			store.Ms(svc.now()), c.Issuer, c.Subject); err != nil {
			return err
		}
		meta.Provider = p.Slug
		if token, err = svc.auth.CreateSessionTx(ctx, tx, userID, users.MethodOIDC, meta); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, users.Actor(u, meta.IP), "user.login", "user:"+userID, audit.OK,
			map[string]any{"method": users.MethodOIDC, "provider": p.Slug, "sso_roles": roles, "listener": meta.Listener})
	})
	var de *deniedError
	if errors.As(err, &de) {
		svc.recordDenied(ctx, p, c, meta, de.reason)
		return "", "", de.pub
	}
	if err != nil {
		return "", "", err
	}
	return token, userID, nil
}

func (svc *Service) recordDenied(ctx context.Context, p *Provider, c *Claims, meta users.SessionMeta, reason string) {
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		return audit.Insert(ctx, tx, audit.Actor{Name: "oidc:" + p.Slug, IP: meta.IP}, "user.login", "", audit.Denied,
			map[string]any{"method": users.MethodOIDC, "provider": p.Slug, "reason": reason,
				"subject_hash": base64.RawURLEncoding.EncodeToString(hashOf("subject", c.Issuer+"|"+c.Subject)[:12])})
	})
	if err != nil {
		svc.log.Error("audit of denied sso login failed", "err", err.Error())
	}
}

// provisionTx creates a JIT user with no manual roles. An email already used by another user is neither merged nor
// reused.
func (svc *Service) provisionTx(ctx context.Context, tx *sql.Tx, p *Provider, c *Claims) (string, error) {
	var email sql.NullString
	if c.EmailVerified && c.Email != "" {
		if taken, err := users.EmailTaken(ctx, tx, c.Email); err != nil {
			return "", err
		} else if taken {
			return "", &deniedError{reason: "jit_email_conflict", pub: users.Errf(users.KindConflict,
				"a user with this email already exists; ask an administrator to link your SSO identity")}
		}
		email = sql.NullString{String: c.Email, Valid: true}
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = c.Email
	}
	if name == "" {
		name = "SSO user"
	}
	if len(name) > 128 {
		name = name[:128]
	}
	id, err := svc.auth.InsertJITUserTx(ctx, tx, email, name, "oidc:"+p.Slug)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_identities (issuer, subject, provider_id, user_id, linked_via, created_at)
		VALUES (?, ?, ?, ?, 'jit', ?)`, c.Issuer, c.Subject, p.ID, id, store.Ms(svc.now())); err != nil {
		return "", err
	}
	return id, audit.Insert(ctx, tx, audit.Actor{Name: "oidc:" + p.Slug}, "user.create", "user:"+id, audit.OK,
		map[string]any{"via": "jit", "provider": p.Slug, "email": email.String})
}

// syncTx replaces the user's SSO-sourced roles with the mapped set; manual roles are untouched. Removing the last
// enabled administrator, or under SSO-only the last SSO-capable one, is refused: the role is kept and audited.
func (svc *Service) syncTx(ctx context.Context, tx *sql.Tx, userID string, roles []string, refs map[string]string) error {
	before, err := users.ListRolesTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	hadSSOAdmin := slices.ContainsFunc(before, func(r users.RoleAssignment) bool { return r.Source == "sso" && r.Role == users.RoleAdministrator })
	if err := users.ReplaceSSORolesTx(ctx, tx, userID, roles, refs); err != nil {
		return err
	}
	if hadSSOAdmin && !slices.Contains(roles, users.RoleAdministrator) {
		n, err := users.CountEnabledAdmins(ctx, tx)
		if err != nil {
			return err
		}
		lockout, err := users.SSOAdminsAtRisk(ctx, tx)
		if err != nil {
			return err
		}
		if n == 0 || lockout {
			if err := users.ReplaceSSORolesTx(ctx, tx, userID, append(slices.Clone(roles), users.RoleAdministrator),
				mergeRef(refs, users.RoleAdministrator, "last-admin-guard")); err != nil {
				return err
			}
			if err := audit.Insert(ctx, tx, audit.Actor{Name: "system:oidc"}, "user.role_sync", "user:"+userID, audit.Denied,
				map[string]any{"reason": "would remove the last administrator"}); err != nil {
				return err
			}
		}
	}
	after, err := users.ListRolesTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if !slices.Equal(before, after) {
		return audit.Insert(ctx, tx, audit.Actor{Name: "system:oidc"}, "user.role_sync", "user:"+userID, audit.OK, map[string]any{"sso_roles": roles})
	}
	return nil
}

func mergeRef(refs map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for a, b := range refs {
		if a != k {
			out[a] = b
		}
	}
	return out
}

// stepUp verifies a fresh upstream login: auth_time at or after the step-up start (minus skew), and acr when
// configured.
func (svc *Service) stepUp(ctx context.Context, p *Provider, st authState, c *Claims, in CallbackInput) (string, error) {
	pr := in.Principal
	if pr == nil || pr.User.ID != st.userID || pr.SessionID != st.sessionDisplay {
		return "", ErrInvalidState
	}
	var uid string
	err := svc.db.R().QueryRowContext(ctx, "SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?", c.Issuer, c.Subject).Scan(&uid)
	if err != nil || uid != pr.User.ID {
		return "", ErrStepUpFailed
	}
	if c.AuthTime == nil {
		if p.StepUpFallback == StepUpLocalTOTP {
			return "", users.Errf(users.KindMFARequired, "this identity provider cannot prove a fresh login; confirm with your Pulse TOTP code")
		}
		return "", ErrStepUpFailed
	}
	if time.Unix(*c.AuthTime, 0).Before(st.created.Add(-svc.opts.Skew)) {
		return "", ErrStepUpFailed
	}
	if len(p.ACRValues) > 0 && !slices.Contains(p.ACRValues, c.ACR) {
		return "", ErrStepUpFailed
	}
	return svc.auth.ReauthExternal(ctx, pr, p.Slug, in.Meta)
}

// testLogin records a successful end-to-end test login and returns the mapping preview without creating a session.
func (svc *Service) testLogin(ctx context.Context, p *Provider, st authState, c *Claims, pr *users.Principal) (*TestResult, error) {
	if !canTest(pr) || pr.User.ID != st.userID {
		return nil, ErrInvalidState
	}
	roles, _, err := svc.mapped(ctx, p, c.Groups)
	if err != nil {
		return nil, err
	}
	res := &TestResult{Claims: *c, MappedRoles: roles}
	if res.Claims.Groups == nil {
		res.Claims.Groups = []string{}
	}
	effective := slices.Clone(roles)
	var uid string
	if err := svc.db.R().QueryRowContext(ctx, "SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?", c.Issuer, c.Subject).
		Scan(&uid); err == nil {
		res.LinkedUserID = uid
		if as, err := users.ListRolesTx(ctx, svc.db.R(), uid); err == nil {
			for _, a := range as {
				if a.Source == "manual" && !slices.Contains(effective, a.Role) {
					effective = append(effective, a.Role)
				}
			}
		}
	} else if p.Provisioning == JIT {
		res.WouldProvision, res.ProvisionReason = jitAllowed(p.JIT, c)
	} else {
		res.ProvisionReason = "not provisioned (preprovisioned mode)"
	}
	slices.Sort(effective)
	res.EffectiveRoles = effective
	err = svc.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE oidc_providers SET test_login_ok_at = ?, test_login_by = ? WHERE id = ?",
			store.Ms(svc.now()), pr.User.ID, p.ID); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, users.Actor(pr.User, ""), "oidc.provider.test_login", "oidc_provider:"+p.ID, audit.OK,
			map[string]any{"mapped_roles": roles, "linked": res.LinkedUserID != ""})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// link binds (iss, sub) to the user who started the flow: a signed-in session on both ends, or an admin-issued link
// invitation. An identity linked to someone else is never moved.
func (svc *Service) link(ctx context.Context, p *Provider, st authState, c *Claims, in CallbackInput) (string, error) {
	uid := st.userID
	if st.hasSession && (in.Principal == nil || in.Principal.SessionID != st.sessionDisplay || in.Principal.User.ID != uid) {
		return "", ErrInvalidState
	}
	via := "self_link"
	if !st.hasSession {
		via = "admin"
	}
	err := svc.db.Write(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, "SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?", c.Issuer, c.Subject).Scan(&existing)
		if err == nil {
			if existing == uid {
				return nil
			}
			return users.Errf(users.KindConflict, "this SSO identity is already linked to another user")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_identities (issuer, subject, provider_id, user_id, linked_via, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, c.Issuer, c.Subject, p.ID, uid, via, store.Ms(svc.now())); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, audit.Actor{Name: "user-id:" + uid, IP: in.Meta.IP}, "user.identity.link", "user:"+uid, audit.OK,
			map[string]any{"provider": p.Slug, "via": via})
	})
	return uid, err
}

// ReauthTOTP is the local-TOTP step-up fallback: only for SSO sessions whose provider is enabled and configured with
// stepup_fallback = local_totp.
func (svc *Service) ReauthTOTP(ctx context.Context, pr *users.Principal, code string, meta users.SessionMeta) (string, error) {
	if pr == nil || pr.Method != users.MethodOIDC || pr.Provider == "" {
		return "", users.ErrTOTPStepUpNotAllowed
	}
	p, err := svc.providerBySlug(ctx, pr.Provider)
	if err != nil && !errors.Is(err, errProviderNotFound) {
		return "", err
	}
	if err != nil || !p.Enabled || p.StepUpFallback != StepUpLocalTOTP {
		return "", users.ErrTOTPStepUpNotAllowed
	}
	return svc.auth.ReauthTOTP(ctx, pr, code, meta)
}
