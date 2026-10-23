package tokens

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

func svc(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func TestLifecycle(t *testing.T) {
	s := svc(t)
	ctx := context.Background()
	tok, secret, err := s.Create(ctx, CreateInput{Name: "ci", Grants: []natsauth.Grant{{Action: natsauth.ActPublish, Subject: "evt.ci.>"}}, CIDRs: []string{"10.1.2.3", "192.168.0.0/16"}}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "pulse_"+tok.Prefix+"_") || len(secret) < 50 || tok.CIDRs[0] != "10.1.2.3/32" {
		t.Fatalf("%s %+v", secret, tok)
	}
	got, err := s.Authenticate(ctx, secret, "192.168.4.5")
	if err != nil || got.ID != tok.ID {
		t.Fatalf("auth: %v", err)
	}
	for name, c := range map[string]struct{ bearer, ip string }{
		"wrong ip":       {secret, "8.8.8.8"},
		"tampered":       {secret[:len(secret)-1] + "x", "10.1.2.3"},
		"unknown prefix": {"pulse_zzzzzzzz_" + secret[16:], "10.1.2.3"},
		"garbage":        {"Bearer nope", "10.1.2.3"},
		"empty":          {"", "10.1.2.3"},
	} {
		if _, err := s.Authenticate(ctx, c.bearer, c.ip); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.Revoke(ctx, tok.ID, audit.System); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, secret, "10.1.2.3"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("revoked token authenticated")
	}
	got, _ = s.Get(ctx, tok.ID)
	if got.Status != "revoked" {
		t.Fatalf("status %s", got.Status)
	}
	if err := s.Revoke(ctx, "nope", audit.System); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
}

func TestExpiryAndValidation(t *testing.T) {
	s := svc(t)
	ctx := context.Background()
	_, secret, err := s.Create(ctx, CreateInput{Name: "short", ExpiresIn: time.Second}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := s.Authenticate(ctx, secret, "1.2.3.4"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired token authenticated")
	}
	for name, in := range map[string]CreateInput{
		"bad name":      {Name: "Bad Name"},
		"durable grant": {Name: "d", Grants: []natsauth.Grant{{Action: natsauth.ActDurable, Subject: "evt.>", Name: "x"}}},
		"bad grant":     {Name: "g", Grants: []natsauth.Grant{{Action: natsauth.ActPublish, Subject: ">"}}},
		"bad cidr":      {Name: "c", CIDRs: []string{"10.0.0.0/99"}},
	} {
		if _, _, err := s.Create(ctx, in, audit.System); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := s.Create(ctx, CreateInput{Name: "short"}, audit.System); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate name accepted")
	}
}
