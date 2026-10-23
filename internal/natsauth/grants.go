// Package natsauth mints NATS operator, account and user JWTs and turns client grants into NATS permissions
// (ADR-0006, ADR-0007, ADR-0025).
package natsauth

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nats-io/jwt/v2"
)

// Action is what a grant allows on its subject.
type Action string

const (
	// ActPublish allows publishing to Subject (JetStream acks come back on the client's inbox).
	ActPublish Action = "publish"
	// ActSubscribe allows core subscriptions on Subject and replay through filtered consumers on Stream.
	ActSubscribe Action = "subscribe"
	// ActRequest allows sending requests to Subject.
	ActRequest Action = "request"
	// ActReply allows serving requests on Subject (subscribe + respond to reply subjects).
	ActReply Action = "reply"
	// ActDurable allows a named durable consumer "<client-id>_<Name>" on Stream filtered to Subject.
	ActDurable Action = "durable"
	// ActBlobs allows reading and writing the blobs Object Store.
	ActBlobs Action = "blobs"
)

// DenyAllSentinel is published as the only allow entry when a client has no publish grants. NATS treats an empty
// allow list as "allow everything not denied", so least privilege (PL-AUTH-003) needs an allow entry that matches
// nothing a client could use. No pulse component ever subscribes to it.
const DenyAllSentinel = "_pulse.no_publish_grants"

// DefaultStream is the stream subscribe and durable grants replay from unless Stream is set.
const DefaultStream = "EVENTS"

// BlobsBucket is the Object Store bucket for large payloads (PL-BUS-004).
const BlobsBucket = "blobs"

// Grant is one permission statement on a client or HTTP token.
type Grant struct {
	Action  Action `json:"action"`
	Subject string `json:"subject,omitempty"`
	Stream  string `json:"stream,omitempty"`
	Name    string `json:"name,omitempty"`
}

var (
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	streamRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	clientIDs = regexp.MustCompile(`^[a-z0-9]{6,32}$`)
)

// ValidateSubjectPattern checks a grant subject: valid NATS tokens with wildcards, no system or inbox prefixes,
// and not a bare wildcard.
func ValidateSubjectPattern(s string) error {
	if s == "" {
		return errors.New("subject is required")
	}
	if len(s) > 256 {
		return errors.New("subject is too long")
	}
	toks := strings.Split(s, ".")
	if toks[0] == ">" || toks[0] == "*" {
		return errors.New("subject must start with a literal token")
	}
	if strings.HasPrefix(s, "$") || strings.HasPrefix(s, "_INBOX") {
		return errors.New("system and inbox subjects cannot be granted")
	}
	if strings.HasPrefix(s, "_sys.") && !strings.HasPrefix(s, "_sys.alert.") && s != "_sys.alert" {
		return errors.New("only _sys.alert.> can be granted under _sys")
	}
	for i, t := range toks {
		switch {
		case t == ">":
			if i != len(toks)-1 {
				return errors.New("'>' must be the last token")
			}
		case t == "*":
		case !tokenRe.MatchString(t):
			return fmt.Errorf("invalid token %q", t)
		}
	}
	return nil
}

// Validate checks a grant's fields for its action.
func (g Grant) Validate() error {
	switch g.Action {
	case ActBlobs:
		if g.Subject != "" || g.Stream != "" || g.Name != "" {
			return errors.New("blobs grant takes no subject, stream or name")
		}
		return nil
	case ActPublish, ActSubscribe, ActRequest, ActReply, ActDurable:
	default:
		return fmt.Errorf("unknown action %q", g.Action)
	}
	if err := ValidateSubjectPattern(g.Subject); err != nil {
		return err
	}
	if g.Stream != "" && !streamRe.MatchString(g.Stream) {
		return fmt.Errorf("invalid stream name %q", g.Stream)
	}
	if g.Action == ActDurable {
		if !nameRe.MatchString(g.Name) {
			return fmt.Errorf("durable name %q must match %s", g.Name, nameRe)
		}
	} else if g.Name != "" {
		return errors.New("only durable grants take a name")
	}
	if (g.Action == ActPublish || g.Action == ActRequest || g.Action == ActReply) && g.Stream != "" {
		return fmt.Errorf("%s grants take no stream", g.Action)
	}
	return nil
}

func (g Grant) stream() string {
	if g.Stream != "" {
		return g.Stream
	}
	return DefaultStream
}

// ValidateClientID checks a client ID usable in inbox and consumer names.
func ValidateClientID(id string) error {
	if !clientIDs.MatchString(id) {
		return fmt.Errorf("invalid client id %q", id)
	}
	return nil
}

// InboxPrefix is the per-client inbox prefix (PL-SDK-007).
func InboxPrefix(clientID string) string { return "_INBOX_" + clientID }

// DurableName is the consumer name a durable grant allows.
func DurableName(clientID, name string) string { return clientID + "_" + name }

// FixedDenies are denied for every client regardless of grants (PL-AUTH-006).
func fixedPubDenies(streams []string, blobs bool) []string {
	d := []string{
		"$SYS.>",
		"$KV.>",
		"$JS.API.STREAM.CREATE.>",
		"$JS.API.STREAM.UPDATE.>",
		"$JS.API.STREAM.DELETE.>",
		"$JS.API.STREAM.PURGE.>",
		"$JS.API.STREAM.MSG.DELETE.>",
		"$JS.API.STREAM.MSG.GET.>",
		"$JS.API.STREAM.SNAPSHOT.>",
		"$JS.API.STREAM.RESTORE.>",
		"$JS.API.STREAM.PEER.>",
		"$JS.API.STREAM.LEADER.>",
		"$JS.API.ACCOUNT.>",
		"$JS.API.SERVER.>",
		"$JS.API.META.>",
		"$JS.API.CONSUMER.DURABLE.CREATE.>",
	}
	for _, s := range streams {
		d = append(d, "$JS.API.DIRECT.GET."+s) // unfiltered direct get (by sequence)
	}
	if !blobs {
		d = append(d, "$O.>")
	}
	return d
}

// Permissions generates the NATS permissions for a client's grants. The output is deterministic (sorted, de-duplicated).
func Permissions(clientID string, grants []Grant) (jwt.Permissions, error) {
	if err := ValidateClientID(clientID); err != nil {
		return jwt.Permissions{}, err
	}
	var pub, sub []string
	streams := map[string]bool{DefaultStream: true}
	blobs := false
	reply := false
	sub = append(sub, InboxPrefix(clientID)+".>")
	for _, g := range grants {
		if err := g.Validate(); err != nil {
			return jwt.Permissions{}, fmt.Errorf("grant %+v: %w", g, err)
		}
		switch g.Action {
		case ActPublish, ActRequest:
			pub = append(pub, g.Subject)
		case ActReply:
			sub = append(sub, g.Subject)
			reply = true
		case ActSubscribe:
			s := g.stream()
			streams[s] = true
			sub = append(sub, g.Subject)
			pub = append(pub,
				"$JS.API.INFO",
				"$JS.API.STREAM.INFO."+s,
				"$JS.API.CONSUMER.CREATE."+s+".*."+g.Subject,
				"$JS.API.CONSUMER.INFO."+s+".*",
				"$JS.API.CONSUMER.MSG.NEXT."+s+".*",
				"$JS.API.DIRECT.GET."+s+"."+g.Subject,
				"$JS.ACK."+s+".>",
				"$JS.FC."+s+".>",
			)
		case ActDurable:
			s := g.stream()
			streams[s] = true
			n := DurableName(clientID, g.Name)
			pub = append(pub,
				"$JS.API.INFO",
				"$JS.API.STREAM.INFO."+s,
				"$JS.API.CONSUMER.CREATE."+s+"."+n+"."+g.Subject,
				"$JS.API.CONSUMER.INFO."+s+"."+n,
				"$JS.API.CONSUMER.MSG.NEXT."+s+"."+n,
				"$JS.API.CONSUMER.DELETE."+s+"."+n,
				"$JS.ACK."+s+"."+n+".>",
				"$JS.FC."+s+"."+n+".>",
			)
		case ActBlobs:
			blobs = true
			obj := "OBJ_" + BlobsBucket
			pub = append(pub,
				"$JS.API.INFO",
				"$JS.API.STREAM.INFO."+obj,
				"$O."+BlobsBucket+".>",
				"$JS.API.CONSUMER.CREATE."+obj+".*.$O."+BlobsBucket+".>",
				"$JS.API.CONSUMER.INFO."+obj+".*",
				"$JS.API.CONSUMER.MSG.NEXT."+obj+".*",
				"$JS.API.DIRECT.GET."+obj+".$O."+BlobsBucket+".>",
				"$JS.ACK."+obj+".>",
				"$JS.FC."+obj+".>",
			)
		}
	}
	streamList := make([]string, 0, len(streams))
	for s := range streams {
		streamList = append(streamList, s)
	}
	slices.Sort(streamList)
	if len(pub) == 0 {
		pub = []string{DenyAllSentinel}
	}
	perms := jwt.Permissions{
		Pub: jwt.Permission{Allow: uniq(pub), Deny: uniq(fixedPubDenies(streamList, blobs))},
		Sub: jwt.Permission{Allow: uniq(sub), Deny: []string{"$SYS.>", "$KV.>"}},
	}
	if reply {
		perms.Resp = &jwt.ResponsePermission{MaxMsgs: 1, Expires: 0}
	}
	return perms, nil
}

func uniq(in []string) jwt.StringList {
	out := slices.Clone(in)
	slices.Sort(out)
	return jwt.StringList(slices.Compact(out))
}
