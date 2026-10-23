package natsauth

import (
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// Account names used in JWTs and logs.
const (
	SysAccountName = "SYS"
	AppAccountName = "APP"
)

// OperatorJWT returns the operator JWT, self-signed by the operator identity key, with one signing key and the system account.
func OperatorJWT(operator, signing nkeys.KeyPair, sysAccountPub string) (string, error) {
	opPub, err := operator.PublicKey()
	if err != nil {
		return "", err
	}
	signPub, err := signing.PublicKey()
	if err != nil {
		return "", err
	}
	oc := jwt.NewOperatorClaims(opPub)
	oc.Name = "pulse"
	oc.SigningKeys.Add(signPub)
	oc.SystemAccount = sysAccountPub
	oc.StrictSigningKeyUsage = true
	return oc.Encode(operator)
}

// ClientScope is one client's scoped signing key with its permissions (ADR-0025).
type ClientScope struct {
	ClientID   string
	SigningKey string // public key of the scoped signer
	Grants     []Grant
}

// Revocation revokes every user JWT for UserKey issued at or before At.
type Revocation struct {
	UserKey string
	At      time.Time
}

// AppAccountSpec is everything the APP account JWT is derived from.
type AppAccountSpec struct {
	SigningKey  string // plain (unscoped) signing key used for pulsed's own user
	Clients     []ClientScope
	Revocations []Revocation
}

// SysAccountClaims returns the SYS account claims.
func SysAccountClaims(accountPub, signingPub string) *jwt.AccountClaims {
	ac := jwt.NewAccountClaims(accountPub)
	ac.Name = SysAccountName
	ac.SigningKeys.Add(signingPub)
	return ac
}

// AppAccountClaims builds the APP account claims: JetStream enabled, one plain signing key, one scoped signer per client,
// revocations.
func AppAccountClaims(accountPub string, spec AppAccountSpec) (*jwt.AccountClaims, error) {
	ac := jwt.NewAccountClaims(accountPub)
	ac.Name = AppAccountName
	ac.SigningKeys.Add(spec.SigningKey)
	ac.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage: jwt.NoLimit,
		DiskStorage:   jwt.NoLimit,
		Streams:       jwt.NoLimit,
		Consumer:      jwt.NoLimit,
		MaxAckPending: jwt.NoLimit,
	}
	for _, c := range spec.Clients {
		perms, err := Permissions(c.ClientID, c.Grants)
		if err != nil {
			return nil, fmt.Errorf("client %s: %w", c.ClientID, err)
		}
		scope := jwt.NewUserScope()
		scope.Key = c.SigningKey
		scope.Role = c.ClientID
		scope.Description = "pulse client " + c.ClientID
		scope.Template = jwt.UserPermissionLimits{
			Permissions: perms,
			Limits: jwt.Limits{
				UserLimits: jwt.UserLimits{},
				NatsLimits: jwt.NatsLimits{Subs: jwt.NoLimit, Data: jwt.NoLimit, Payload: jwt.NoLimit},
			},
			AllowedConnectionTypes: jwt.StringList{jwt.ConnectionTypeStandard, jwt.ConnectionTypeWebsocket},
		}
		ac.SigningKeys.AddScopedSigner(scope)
	}
	for _, r := range spec.Revocations {
		ac.RevokeAt(r.UserKey, r.At)
	}
	return ac, nil
}

// EncodeAccount signs account claims with the operator signing key.
func EncodeAccount(ac *jwt.AccountClaims, operatorSigning nkeys.KeyPair) (string, error) {
	return ac.Encode(operatorSigning)
}

// UserOptions configures a user JWT.
type UserOptions struct {
	Name    string
	Expires time.Time // zero means no expiry
	Tags    []string
	// Scoped must be set when signer is a scoped signing key: the user then carries a zero-valued permission and
	// limits section (jwt's HasEmptyPermissions), and the scope template supplies everything.
	Scoped bool
}

// UserJWT mints a user JWT for userPub in account accountPub, signed by an account signing key (plain or scoped).
// Users signed by a scoped key must not carry permissions; this function never sets any.
func UserJWT(userPub, accountPub string, signer nkeys.KeyPair, o UserOptions) (string, error) {
	uc := jwt.NewUserClaims(userPub)
	uc.Name = o.Name
	uc.IssuerAccount = accountPub
	if o.Scoped {
		uc.UserPermissionLimits = jwt.UserPermissionLimits{}
	}
	if !o.Expires.IsZero() {
		uc.Expires = o.Expires.Unix()
	}
	for _, t := range o.Tags {
		uc.Tags.Add(t)
	}
	return uc.Encode(signer)
}

// Creds renders a .creds file.
func Creds(userJWT string, userSeed []byte) ([]byte, error) {
	return jwt.FormatUserConfig(userJWT, userSeed)
}

// IssuedClient is the result of minting credentials for a client.
type IssuedClient struct {
	UserKey    string // user public key
	SigningKey string // scoped signer public key (its seed is discarded)
	Creds      []byte // shown once
	Expires    time.Time
}

// IssueClient creates a fresh scoped signing key and user key for a client and mints its creds.
// The scoped signer's seed is discarded after signing (ADR-0025); the caller must add
// ClientScope{SigningKey: …} to the APP account and push it before the creds can connect.
func IssueClient(clientID, name, appAccountPub string, expires time.Time) (*IssuedClient, error) {
	if err := ValidateClientID(clientID); err != nil {
		return nil, err
	}
	scoped, err := nkeys.CreateAccount()
	if err != nil {
		return nil, err
	}
	defer scoped.Wipe()
	user, err := nkeys.CreateUser()
	if err != nil {
		return nil, err
	}
	defer user.Wipe()
	scopedPub, _ := scoped.PublicKey()
	userPub, _ := user.PublicKey()
	seed, err := user.Seed()
	if err != nil {
		return nil, err
	}
	tok, err := UserJWT(userPub, appAccountPub, scoped, UserOptions{Name: name, Expires: expires, Tags: []string{"pulse-client:" + clientID}, Scoped: true})
	if err != nil {
		return nil, err
	}
	creds, err := Creds(tok, seed)
	if err != nil {
		return nil, err
	}
	return &IssuedClient{UserKey: userPub, SigningKey: scopedPub, Creds: creds, Expires: expires}, nil
}

// AccountEquivalent reports whether two account JWTs carry the same account content (signing keys, scopes, limits,
// revocations, exports/imports), ignoring issue time, JWT ID and issuer.
func AccountEquivalent(a, b string) bool {
	ca, err := jwt.DecodeAccountClaims(a)
	if err != nil {
		return false
	}
	cb, err := jwt.DecodeAccountClaims(b)
	if err != nil {
		return false
	}
	ja, err1 := jsonOf(ca.Account)
	jb, err2 := jsonOf(cb.Account)
	return err1 == nil && err2 == nil && ca.Subject == cb.Subject && ca.Name == cb.Name && ja == jb
}
