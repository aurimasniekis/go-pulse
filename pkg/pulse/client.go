// Package pulse is the Go SDK for Pulse (PL-SDK-001): publish, subscribe and replay events over NATS JetStream.
//
//	c, err := pulse.Connect("tls://r.events.example.com:4222", pulse.WithCredsFile("bot.creds"), pulse.WithSource("bot"))
//	ack, err := c.Publish(ctx, "evt.home.door.opened", "home.door.opened.v1", map[string]any{"door": "front"})
//	sub, err := c.Subscribe(ctx, "evt.home.>", pulse.Handle(func(ctx context.Context, e pulse.Event, v Door) error {…}),
//		pulse.Since(time.Now().Add(-24*time.Hour)))
package pulse

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// Event is a Pulse event.
type Event = envelope.Event

// DefaultStream is the stream events are stored in.
const DefaultStream = "EVENTS"

// clientTagPrefix marks the client ID in user JWT tags (set by pulsed).
const clientTagPrefix = "pulse-client:"

// Client is a connection to Pulse.
type Client struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	source   string
	stream   string
	clientID string
	ownConn  bool
}

type options struct {
	creds    []byte
	credsErr error
	caPEM    []byte
	caErr    error
	source   string
	name     string
	stream   string
	natsOpts []nats.Option
}

// Option configures Connect.
type Option func(*options)

// WithCredsFile authenticates with a .creds file.
func WithCredsFile(path string) Option {
	return func(o *options) {
		b, err := os.ReadFile(path) //nolint:gosec // caller-supplied creds path
		o.creds, o.credsErr = b, err
	}
}

// WithCreds authenticates with .creds content.
func WithCreds(creds []byte) Option { return func(o *options) { o.creds = creds } }

// WithCAFile trusts an extra CA (the internal CA while NATS still serves the bootstrap certificate).
func WithCAFile(path string) Option {
	return func(o *options) {
		b, err := os.ReadFile(path) //nolint:gosec // caller-supplied CA path
		o.caPEM, o.caErr = b, err
	}
}

// WithCA trusts an extra CA given as PEM.
func WithCA(pem []byte) Option { return func(o *options) { o.caPEM = pem } }

// WithSource sets ce-source on published events.
func WithSource(source string) Option { return func(o *options) { o.source = source } }

// WithName sets the NATS connection name.
func WithName(name string) Option { return func(o *options) { o.name = name } }

// WithStream overrides the stream for subscriptions (default EVENTS).
func WithStream(stream string) Option { return func(o *options) { o.stream = stream } }

// WithNATSOptions passes extra nats.go options.
func WithNATSOptions(opts ...nats.Option) Option {
	return func(o *options) { o.natsOpts = append(o.natsOpts, opts...) }
}

// ClientIDFromCreds extracts the Pulse client ID from the user JWT tags in .creds content.
func ClientIDFromCreds(creds []byte) (string, error) {
	tok, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		return "", err
	}
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		return "", err
	}
	for _, t := range uc.Tags {
		if id, ok := strings.CutPrefix(t, clientTagPrefix); ok {
			return id, nil
		}
	}
	return "", errors.New("pulse: creds carry no pulse client tag")
}

// Connect dials Pulse. The client's private inbox prefix is derived from the creds (PL-SDK-007).
func Connect(url string, opts ...Option) (*Client, error) {
	o := options{stream: DefaultStream}
	for _, fn := range opts {
		fn(&o)
	}
	if o.credsErr != nil {
		return nil, fmt.Errorf("pulse: read creds: %w", o.credsErr)
	}
	if o.caErr != nil {
		return nil, fmt.Errorf("pulse: read CA: %w", o.caErr)
	}
	if len(o.creds) == 0 {
		return nil, errors.New("pulse: creds are required (WithCredsFile or WithCreds)")
	}
	id, err := ClientIDFromCreds(o.creds)
	if err != nil {
		return nil, err
	}
	tok, err := jwt.ParseDecoratedJWT(o.creds)
	if err != nil {
		return nil, err
	}
	kp, err := nkeys.ParseDecoratedNKey(o.creds)
	if err != nil {
		return nil, err
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, err
	}
	nopts := []nats.Option{
		nats.UserJWTAndSeed(tok, string(seed)),
		nats.CustomInboxPrefix("_INBOX_" + id),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	}
	if o.name != "" {
		nopts = append(nopts, nats.Name(o.name))
	} else if o.source != "" {
		nopts = append(nopts, nats.Name(o.source))
	}
	if len(o.caPEM) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(o.caPEM) {
			return nil, errors.New("pulse: invalid CA PEM")
		}
		nopts = append(nopts, nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))
	}
	nopts = append(nopts, o.natsOpts...)
	nc, err := nats.Connect(url, nopts...)
	if err != nil {
		return nil, err
	}
	c, err := New(nc, o.source)
	if err != nil {
		nc.Close()
		return nil, err
	}
	c.clientID, c.stream, c.ownConn = id, o.stream, true
	return c, nil
}

// New wraps an existing connection (e.g. pulsed internals or tests). The caller keeps ownership of nc.
func New(nc *nats.Conn, source string) (*Client, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	return &Client{nc: nc, js: js, source: source, stream: DefaultStream}, nil
}

// Conn exposes the underlying NATS connection.
func (c *Client) Conn() *nats.Conn { return c.nc }

// JetStream exposes the JetStream context.
func (c *Client) JetStream() jetstream.JetStream { return c.js }

// ClientID is the Pulse client ID from the creds ("" for wrapped connections).
func (c *Client) ClientID() string { return c.clientID }

// Close closes the connection if Connect opened it.
func (c *Client) Close() {
	if c.ownConn {
		_ = c.nc.Drain()
	}
}

// PubAck is the stream acknowledgement for a published event.
type PubAck struct {
	ID        string
	Stream    string
	Sequence  uint64
	Duplicate bool
}

type pubOptions struct {
	id          string
	t           time.Time
	contentType string
	source      string
	extra       nats.Header
	blob        *envelope.BlobRef
}

// PublishOption configures one publish.
type PublishOption func(*pubOptions)

// WithID sets the event ID (Nats-Msg-Id); retries with the same ID within 2 minutes are de-duplicated.
func WithID(id string) PublishOption { return func(p *pubOptions) { p.id = id } }

// WithTime sets ce-time (defaults to now).
func WithTime(t time.Time) PublishOption { return func(p *pubOptions) { p.t = t } }

// WithContentType overrides the content type.
func WithContentType(ct string) PublishOption { return func(p *pubOptions) { p.contentType = ct } }

// WithEventSource overrides ce-source for one event.
func WithEventSource(s string) PublishOption { return func(p *pubOptions) { p.source = s } }

// WithHeader adds an extra header.
func WithHeader(k, v string) PublishOption {
	return func(p *pubOptions) {
		if p.extra == nil {
			p.extra = nats.Header{}
		}
		p.extra.Add(k, v)
	}
}

// WithBlob attaches a blob reference (see PutBlob).
func WithBlob(ref envelope.BlobRef) PublishOption { return func(p *pubOptions) { p.blob = &ref } }

// encodeData turns a value into bytes and a default content type.
func encodeData(data any) ([]byte, string, error) {
	switch v := data.(type) {
	case nil:
		return nil, "", nil
	case []byte:
		return v, envelope.ContentBinary, nil
	case string:
		return []byte(v), envelope.ContentText, nil
	case json.RawMessage:
		return v, envelope.ContentJSON, nil
	default:
		b, err := json.Marshal(v)
		return b, envelope.ContentJSON, err
	}
}

// Publish stores an event in JetStream and waits for the acknowledgement. data may be []byte, string,
// json.RawMessage, nil or any JSON-marshalable value. typ is ce-type ("" publishes an untyped event).
func (c *Client) Publish(ctx context.Context, subject, typ string, data any, opts ...PublishOption) (*PubAck, error) {
	var p pubOptions
	for _, fn := range opts {
		fn(&p)
	}
	body, ct, err := encodeData(data)
	if err != nil {
		return nil, err
	}
	if p.contentType != "" {
		ct = p.contentType
	}
	src := c.source
	if p.source != "" {
		src = p.source
	}
	msg, err := envelope.Encode(envelope.Event{
		Subject: subject, ID: p.id, Type: typ, Source: src, Time: p.t, ContentType: ct, Data: body, Extra: p.extra, Blob: p.blob,
	})
	if err != nil {
		return nil, err
	}
	ack, err := c.js.PublishMsg(ctx, msg)
	if err != nil {
		return nil, err
	}
	return &PubAck{ID: msg.Header.Get(envelope.HeaderID), Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// Request sends a core NATS request (request/reply services) and returns the reply payload.
func (c *Client) Request(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
	return c.nc.RequestWithContext(ctx, subject, data)
}
