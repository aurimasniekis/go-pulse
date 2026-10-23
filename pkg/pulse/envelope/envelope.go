// Package envelope is the Pulse event envelope (CloudEvents-lite in NATS headers, ADR-0005, docs/ENVELOPE.md).
// It is the single Go implementation shared by pulsed, the SDK and the CLI (PL-ENV-002).
package envelope

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/oklog/ulid/v2"
)

// Header names.
const (
	HeaderID          = "Nats-Msg-Id"
	HeaderType        = "ce-type"
	HeaderSource      = "ce-source"
	HeaderTime        = "ce-time"
	HeaderContentType = "content-type"
	HeaderBlob        = "pulse-blob"
	HeaderBlobSize    = "pulse-blob-size"
	HeaderBlobSHA256  = "pulse-blob-sha256"
)

// Content types used by the SDKs.
const (
	ContentJSON   = "application/json"
	ContentText   = "text/plain; charset=utf-8"
	ContentBinary = "application/octet-stream"
)

// SubjectPrefix is the root of every event subject.
const SubjectPrefix = "evt."

// MinTokens is the minimum number of subject tokens: evt.<domain>.<entity>.<action>.
const MinTokens = 4

// MaxSubjectLen bounds subject length.
const MaxSubjectLen = 256

var tokenRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ErrSubject is returned for subjects that break the grammar (PL-BUS-002).
var ErrSubject = errors.New("invalid event subject")

// ValidateSubject checks evt.<domain>.<entity>.<action>[.<key>…]: at least 4 tokens, each [a-z0-9_-]+, no wildcards.
func ValidateSubject(s string) error {
	if len(s) > MaxSubjectLen {
		return fmt.Errorf("%w: longer than %d bytes", ErrSubject, MaxSubjectLen)
	}
	if !strings.HasPrefix(s, SubjectPrefix) {
		return fmt.Errorf("%w %q: must start with %q", ErrSubject, s, SubjectPrefix)
	}
	toks := strings.Split(s, ".")
	if len(toks) < MinTokens {
		return fmt.Errorf("%w %q: need evt.<domain>.<entity>.<action> (at least %d tokens)", ErrSubject, s, MinTokens)
	}
	for _, t := range toks {
		if !tokenRe.MatchString(t) {
			return fmt.Errorf("%w %q: token %q must match [a-z0-9_-]+", ErrSubject, s, t)
		}
	}
	return nil
}

// ValidateFilter checks a subscription filter under evt.>: tokens are [a-z0-9_-]+ or the wildcards * and > (last).
func ValidateFilter(f string) error {
	if f == "evt.>" {
		return nil
	}
	if !strings.HasPrefix(f, SubjectPrefix) {
		return fmt.Errorf("%w %q: filters must start with %q", ErrSubject, f, SubjectPrefix)
	}
	toks := strings.Split(f, ".")
	for i, t := range toks {
		switch {
		case t == ">" && i == len(toks)-1:
		case t == "*":
		case tokenRe.MatchString(t):
		default:
			return fmt.Errorf("%w %q: bad token %q", ErrSubject, f, t)
		}
	}
	return nil
}

// BlobRef points at an Object Store object holding a payload larger than max_payload (PL-ENV-004).
type BlobRef struct {
	Bucket string `json:"bucket"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Event is a decoded or to-be-encoded event.
type Event struct {
	Subject     string
	ID          string
	Type        string // empty means untyped
	Source      string
	Time        time.Time
	ContentType string
	Data        []byte
	Blob        *BlobRef
	// Extra holds headers that are not envelope fields.
	Extra nats.Header

	// Read-side metadata (set by consumers, ignored by Encode).
	Stream     string
	Sequence   uint64
	StreamTime time.Time
}

// Untyped reports whether the event lacks ce-type (e.g. a raw `nats pub`).
func (e *Event) Untyped() bool { return e.Type == "" }

// NewID returns a new ULID (lowercase-insensitive, sortable), used as Nats-Msg-Id.
func NewID() string { return ulid.MustNew(ulid.Now(), rand.Reader).String() }

// TimeFormat is the ce-time format: RFC 3339 with nanoseconds, UTC.
const TimeFormat = time.RFC3339Nano

// Encode builds a NATS message. It fills ID and Time when empty and validates the subject.
func Encode(e Event) (*nats.Msg, error) {
	if err := ValidateSubject(e.Subject); err != nil {
		return nil, err
	}
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	m := nats.NewMsg(e.Subject)
	for k, vs := range e.Extra {
		for _, v := range vs {
			m.Header.Add(k, v)
		}
	}
	m.Header.Set(HeaderID, e.ID)
	if e.Type != "" {
		m.Header.Set(HeaderType, e.Type)
	}
	if e.Source != "" {
		m.Header.Set(HeaderSource, e.Source)
	}
	m.Header.Set(HeaderTime, e.Time.UTC().Format(TimeFormat))
	if e.ContentType != "" {
		m.Header.Set(HeaderContentType, e.ContentType)
	}
	if e.Blob != nil {
		m.Header.Set(HeaderBlob, e.Blob.Bucket+"/"+e.Blob.Name)
		m.Header.Set(HeaderBlobSize, strconv.FormatInt(e.Blob.Size, 10))
		if e.Blob.SHA256 != "" {
			m.Header.Set(HeaderBlobSHA256, e.Blob.SHA256)
		}
	}
	m.Data = e.Data
	return m, nil
}

var envelopeHeaders = map[string]bool{
	HeaderID: true, HeaderType: true, HeaderSource: true, HeaderTime: true, HeaderContentType: true,
	HeaderBlob: true, HeaderBlobSize: true, HeaderBlobSHA256: true,
}

// Decode reads the envelope from a message's subject, headers and data. It never fails: missing or malformed fields are
// left empty (untyped events are valid, PL-BUS-003).
func Decode(subject string, h nats.Header, data []byte) Event {
	e := Event{Subject: subject, Data: data}
	e.ID = h.Get(HeaderID)
	e.Type = h.Get(HeaderType)
	e.Source = h.Get(HeaderSource)
	e.ContentType = h.Get(HeaderContentType)
	if t, err := time.Parse(TimeFormat, h.Get(HeaderTime)); err == nil {
		e.Time = t.UTC()
	}
	if ref := h.Get(HeaderBlob); ref != "" {
		if bucket, name, ok := strings.Cut(ref, "/"); ok {
			size, _ := strconv.ParseInt(h.Get(HeaderBlobSize), 10, 64)
			e.Blob = &BlobRef{Bucket: bucket, Name: name, Size: size, SHA256: h.Get(HeaderBlobSHA256)}
		}
	}
	for k, vs := range h {
		if envelopeHeaders[k] || strings.HasPrefix(k, "Nats-") {
			continue
		}
		if e.Extra == nil {
			e.Extra = nats.Header{}
		}
		e.Extra[k] = append([]string(nil), vs...)
	}
	return e
}

// DecodeMsg decodes a core NATS message.
func DecodeMsg(m *nats.Msg) Event { return Decode(m.Subject, m.Header, m.Data) }
