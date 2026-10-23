package natsauth

import (
	"slices"
	"testing"

	"github.com/nats-io/jwt/v2"
)

const cid = "01hzzabcdeabcdeabcdeabcdea"

func TestValidateGrant(t *testing.T) {
	good := []Grant{
		{Action: ActPublish, Subject: "evt.home.door.opened"},
		{Action: ActSubscribe, Subject: "evt.home.>"},
		{Action: ActSubscribe, Subject: "evt.*.door.*", Stream: "EVENTS"},
		{Action: ActSubscribe, Subject: "_sys.alert.>"},
		{Action: ActRequest, Subject: "svc.weather.get"},
		{Action: ActReply, Subject: "svc.weather.>"},
		{Action: ActDurable, Subject: "evt.home.>", Name: "indexer"},
		{Action: ActBlobs},
	}
	for _, g := range good {
		if err := g.Validate(); err != nil {
			t.Errorf("%+v: %v", g, err)
		}
	}
	bad := []Grant{
		{Action: "admin", Subject: "evt.>"},
		{Action: ActPublish, Subject: ">"},
		{Action: ActPublish, Subject: "*.x"},
		{Action: ActPublish, Subject: "$JS.API.STREAM.DELETE.EVENTS"},
		{Action: ActSubscribe, Subject: "$KV.x.>"},
		{Action: ActSubscribe, Subject: "_INBOX.>"},
		{Action: ActSubscribe, Subject: "_sys.audit.>"},
		{Action: ActPublish, Subject: "evt.>.x"},
		{Action: ActPublish, Subject: "evt..x"},
		{Action: ActPublish, Subject: "evt.a b"},
		{Action: ActPublish, Subject: "evt.x", Stream: "EVENTS"},
		{Action: ActDurable, Subject: "evt.x", Name: "Bad Name"},
		{Action: ActSubscribe, Subject: "evt.x", Name: "n"},
		{Action: ActBlobs, Subject: "evt.x"},
		{Action: ActSubscribe, Subject: "evt.x", Stream: "bad.stream"},
	}
	for _, g := range bad {
		if err := g.Validate(); err == nil {
			t.Errorf("%+v accepted", g)
		}
	}
}

func has(l jwt.StringList, s string) bool { return slices.Contains(l, s) }

func TestPermissionsNoGrants(t *testing.T) {
	p, err := Permissions(cid, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An empty allow list would mean "allow everything not denied".
	if len(p.Pub.Allow) != 1 || p.Pub.Allow[0] != DenyAllSentinel {
		t.Fatalf("pub allow %v", p.Pub.Allow)
	}
	if len(p.Sub.Allow) != 1 || p.Sub.Allow[0] != "_INBOX_"+cid+".>" {
		t.Fatalf("sub allow %v", p.Sub.Allow)
	}
	for _, d := range []string{"$KV.>", "$O.>", "$SYS.>", "$JS.API.STREAM.DELETE.>", "$JS.API.DIRECT.GET.EVENTS", "$JS.API.STREAM.MSG.GET.>"} {
		if !has(p.Pub.Deny, d) {
			t.Errorf("missing deny %s", d)
		}
	}
	if p.Resp != nil {
		t.Fatal("responses allowed without a reply grant")
	}
}

func TestPermissionsByAction(t *testing.T) {
	p, err := Permissions(cid, []Grant{
		{Action: ActPublish, Subject: "evt.a.>"},
		{Action: ActSubscribe, Subject: "evt.b.>"},
		{Action: ActDurable, Subject: "evt.c.>", Name: "idx"},
		{Action: ActReply, Subject: "svc.x"},
		{Action: ActBlobs},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPub := []string{
		"evt.a.>",
		"$JS.API.CONSUMER.CREATE.EVENTS.*.evt.b.>",
		"$JS.API.DIRECT.GET.EVENTS.evt.b.>",
		"$JS.API.CONSUMER.CREATE.EVENTS." + cid + "_idx.evt.c.>",
		"$JS.API.CONSUMER.DELETE.EVENTS." + cid + "_idx",
		"$O.blobs.>",
	}
	for _, w := range wantPub {
		if !has(p.Pub.Allow, w) {
			t.Errorf("missing pub allow %s", w)
		}
	}
	for _, w := range []string{"evt.b.>", "svc.x", "_INBOX_" + cid + ".>"} {
		if !has(p.Sub.Allow, w) {
			t.Errorf("missing sub allow %s", w)
		}
	}
	if has(p.Pub.Deny, "$O.>") {
		t.Error("blobs grant still denies $O.>")
	}
	if has(p.Pub.Allow, "$JS.API.CONSUMER.DELETE.EVENTS.*") {
		t.Error("ephemeral grants must not delete arbitrary consumers")
	}
	if p.Resp == nil || p.Resp.MaxMsgs != 1 {
		t.Error("reply grant without response permission")
	}
	// Deterministic: same input, same output order.
	q, _ := Permissions(cid, []Grant{
		{Action: ActBlobs},
		{Action: ActReply, Subject: "svc.x"},
		{Action: ActDurable, Subject: "evt.c.>", Name: "idx"},
		{Action: ActSubscribe, Subject: "evt.b.>"},
		{Action: ActPublish, Subject: "evt.a.>"},
	})
	if !slices.Equal(p.Pub.Allow, q.Pub.Allow) || !slices.Equal(p.Sub.Allow, q.Sub.Allow) || !slices.Equal(p.Pub.Deny, q.Pub.Deny) {
		t.Fatal("permissions depend on grant order")
	}
}

func TestPermissionsRejectsBadInput(t *testing.T) {
	if _, err := Permissions("BAD ID", nil); err == nil {
		t.Fatal("bad client id accepted")
	}
	if _, err := Permissions(cid, []Grant{{Action: ActPublish, Subject: ">"}}); err == nil {
		t.Fatal("bad grant accepted")
	}
}

func TestAccountEquivalent(t *testing.T) {
	// Covered end to end in internal/clients; here only the negative path on garbage input.
	if AccountEquivalent("x", "y") {
		t.Fatal("garbage considered equivalent")
	}
}
