package clients

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

type fixture struct {
	env *natstest.Env
	sys *sysapi.Client
	svc *Service
	db  *store.DB
}

func setup(t *testing.T, opts ...natstest.Options) *fixture {
	t.Helper()
	env := natstest.Start(t, opts...)
	db, err := store.Open(context.Background(), store.Options{Path: env.Layout.DatabaseFile()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sys := sysapi.New(env.SysConn())
	svc := New(db, env.Keys, sys, nil)
	if _, err := svc.Sync(context.Background(), false); err != nil {
		t.Fatalf("initial APP push: %v", err)
	}
	return &fixture{env: env, sys: sys, svc: svc, db: db}
}

var admin = audit.Actor{Name: "admin", IP: "127.0.0.1"}

func errCollector() (nats.Option, <-chan error) {
	ch := make(chan error, 16)
	return nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		select {
		case ch <- err:
		default:
		}
	}), ch
}

func expectViolation(t *testing.T, ch <-chan error, want string) {
	t.Helper()
	select {
	case err := <-ch:
		if !strings.Contains(err.Error(), "Permissions Violation") || !strings.Contains(err.Error(), want) {
			t.Fatalf("unexpected async error %v (want violation on %s)", err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no permissions violation for %s", want)
	}
}

func expectNoViolation(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("unexpected async error: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

// PL-TST-003: creds connect with least privilege.
func TestCredsConnectAndLeastPrivilege(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c, creds, err := f.svc.Create(ctx, CreateInput{Name: "bot", Grants: []natsauth.Grant{
		{Action: natsauth.ActPublish, Subject: "evt.test.>"},
		{Action: natsauth.ActSubscribe, Subject: "evt.test.>"},
	}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(creds), "NATS USER JWT") || !strings.Contains(string(creds), "USER NKEY SEED") {
		t.Fatal("creds file malformed")
	}
	eh, errs := errCollector()
	nc, err := f.env.ConnectCreds(creds, eh, nats.CustomInboxPrefix(c.InboxPrefix()))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	sub, err := nc.SubscribeSync("evt.test.>")
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish("evt.test.thing.created", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.NextMsg(2 * time.Second); err != nil {
		t.Fatalf("allowed pub/sub round trip: %v", err)
	}
	expectNoViolation(t, errs)

	_ = nc.Publish("evt.other.thing.created", nil)
	expectViolation(t, errs, "evt.other.thing.created")
	_ = nc.Publish("$JS.API.STREAM.DELETE.EVENTS", nil)
	expectViolation(t, errs, "$JS.API.STREAM.DELETE.EVENTS")
	_ = nc.Publish("$KV.anything.x", nil)
	expectViolation(t, errs, "$KV.anything.x")
	if _, err := nc.SubscribeSync("_INBOX.other.>"); err == nil {
		expectViolation(t, errs, "_INBOX.other.>")
	}

	// A client with no grants can connect but do nothing (PL-AUTH-003).
	_, creds2, err := f.svc.Create(ctx, CreateInput{Name: "empty"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	eh2, errs2 := errCollector()
	nc2, err := f.env.ConnectCreds(creds2, eh2)
	if err != nil {
		t.Fatal(err)
	}
	defer nc2.Close()
	_ = nc2.Publish("evt.test.thing.created", nil)
	expectViolation(t, errs2, "evt.test.thing.created")
}

// PL-TST-003 / PL-BAN-001: auth errors carry the client host.
func TestAuthErrorAdvisoryHasHost(t *testing.T) {
	f := setup(t)
	got := make(chan sysapi.Advisory, 4)
	sub, err := f.sys.Subscribe(sysapi.SubjAuthErr, func(a sysapi.Advisory) { got <- a })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	_ = f.env.SysConn().Flush()

	// A user claiming the APP account but signed by a key the account does not know must be rejected.
	stranger, _ := nkeys.CreateAccount()
	user, _ := nkeys.CreateUser()
	upub, _ := user.PublicKey()
	tok, _ := natsauth.UserJWT(upub, f.env.Keys.Pub(f.env.Keys.AppAccount), stranger, natsauth.UserOptions{Name: "intruder"})
	seed, _ := user.Seed()
	creds, _ := natsauth.Creds(tok, seed)
	if _, err := f.env.ConnectCreds(creds); !natstest.IsAuthErr(err) {
		t.Fatalf("stranger connected or wrong error: %v", err)
	}
	select {
	case a := <-got:
		if a.Client.Host != "127.0.0.1" {
			t.Fatalf("advisory host %q", a.Client.Host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no CLIENT.AUTH.ERR advisory")
	}
}

// PL-AUTH-007 / G-2: revoke disconnects within 5 s and the creds stop working.
func TestRevokeDisconnects(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c, creds, err := f.svc.Create(ctx, CreateInput{Name: "doomed", Grants: []natsauth.Grant{{Action: natsauth.ActSubscribe, Subject: "evt.x.>"}}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	nc, err := f.env.ConnectCreds(creds, nats.ClosedHandler(func(*nats.Conn) { close(closed) }))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if conns, _ := f.svc.Connections(ctx, c.UserKey); len(conns) != 1 {
		t.Fatalf("connections before revoke: %d", len(conns))
	}
	start := time.Now()
	if err := f.svc.Revoke(ctx, c.ID, admin); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
		t.Logf("disconnected %v after revoke", time.Since(start))
	case <-time.After(5 * time.Second):
		t.Fatal("client still connected 5 s after revoke")
	}
	if _, err := f.env.ConnectCreds(creds); !natstest.IsAuthErr(err) {
		t.Fatalf("revoked creds: %v", err)
	}
	got, _ := f.svc.Get(ctx, c.ID)
	if got.Status != StatusRevoked {
		t.Fatalf("status %s", got.Status)
	}
	var n int
	_ = f.db.R().QueryRowContext(ctx, "SELECT COUNT(*) FROM audit WHERE action = 'client.revoke' AND target = ?", c.ID).Scan(&n)
	if n != 1 {
		t.Fatal("revoke not audited")
	}
}

// PL-AUTH-008: rotation invalidates the previous creds and issues working new ones.
func TestRotate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c, old, err := f.svc.Create(ctx, CreateInput{Name: "rot"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	_, fresh, err := f.svc.Rotate(ctx, c.ID, 0, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.ConnectCreds(old); !natstest.IsAuthErr(err) {
		t.Fatalf("old creds after rotate: %v", err)
	}
	nc, err := f.env.ConnectCreds(fresh)
	if err != nil {
		t.Fatalf("new creds: %v", err)
	}
	nc.Close()
}

// PL-TST-003: accounts persist over a server restart (resolver dir) without a re-push.
func TestAccountsPersistOverRestart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, creds, err := f.svc.Create(ctx, CreateInput{Name: "survivor"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	f.env.Restart()
	nc, err := f.env.ConnectCreds(creds)
	if err != nil {
		t.Fatalf("connect after restart: %v", err)
	}
	nc.Close()
	sys := sysapi.New(f.env.SysConn())
	svc := New(f.db, f.env.Keys, sys, nil)
	pushed, err := svc.Sync(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if pushed {
		t.Fatal("account was considered stale after restart")
	}
}

// PL-AUTH-009 (behavior record): a grant edit is pushed through the scoped key; check what connected clients see.
func TestGrantEditAppliesWithoutNewCreds(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c, creds, err := f.svc.Create(ctx, CreateInput{Name: "editable"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateGrants(ctx, c.ID, []natsauth.Grant{{Action: natsauth.ActPublish, Subject: "evt.edit.>"}}, c.Version, admin); err != nil {
		t.Fatal(err)
	}
	eh, errs := errCollector()
	nc, err := f.env.ConnectCreds(creds, eh)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_ = nc.Publish("evt.edit.a.b", nil)
	expectNoViolation(t, errs)
	// Stale version is rejected.
	if _, err := f.svc.UpdateGrants(ctx, c.ID, nil, c.Version, admin); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
}

// PL-TST-003: RELOAD with a new certificate is served to new connections.
func TestReloadServesNewCert(t *testing.T) {
	f := setup(t, natstest.Options{Process: true})
	ctx := context.Background()
	served := func() string {
		var serial string
		tc := f.env.ClientTLS()
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			serial = certs.SerialHex(cs.PeerCertificates[0])
			return nil
		}
		tok, seed, _ := f.env.Keys.SysUser()
		nc, err := nats.Connect(f.env.ClientURL, nats.Secure(tc), nats.UserJWTAndSeed(tok, string(seed)))
		if err != nil {
			t.Fatal(err)
		}
		nc.Close()
		return serial
	}
	before := served()
	ca, _ := f.env.Keys.CA()
	leaf, err := ca.IssueServer([]string{"localhost", "127.0.0.1"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := secretbox.WriteFileAtomic(f.env.Layout.ServerKey(), leaf.KeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secretbox.WriteFileAtomic(f.env.Layout.ServerCert(), leaf.CertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.sys.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	after := served()
	if after == before || after != certs.SerialHex(leaf.Cert) {
		t.Fatalf("serial before %s after %s want %s", before, after, certs.SerialHex(leaf.Cert))
	}
}

// ADR-0025: a grant edit disconnects the client's current connections (the server drops users of a changed scoped
// signer); a reconnecting client comes back with the new permissions and keeps its creds.
func TestGrantEditReconnectsConnectedClient(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c, creds, err := f.svc.Create(ctx, CreateInput{Name: "live"}, admin)
	if err != nil {
		t.Fatal(err)
	}
	eh, errs := errCollector()
	reconnected := make(chan struct{}, 1)
	allowReconnect := func(o *nats.Options) error {
		o.AllowReconnect, o.MaxReconnect, o.ReconnectWait = true, -1, 100*time.Millisecond
		return nil
	}
	nc, err := f.env.ConnectCreds(creds, eh, allowReconnect, nats.ReconnectHandler(func(*nats.Conn) { reconnected <- struct{}{} }))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_ = nc.Publish("evt.live.a.b", nil)
	expectViolation(t, errs, "evt.live.a.b")
	if _, err := f.svc.UpdateGrants(ctx, c.ID, []natsauth.Grant{{Action: natsauth.ActPublish, Subject: "evt.live.>"}}, c.Version, admin); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not reconnect after grant edit")
	}
	for len(errs) > 0 {
		t.Logf("async error around reconnect (expected): %v", <-errs)
	}
	_ = nc.Publish("evt.live.a.b", nil)
	expectNoViolation(t, errs)
}
