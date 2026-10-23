package api

import (
	"encoding/base64"
	"time"
	"unicode/utf8"

	"github.com/nats-io/nats.go"

	"github.com/aurimasniekis/go-pulse/internal/query"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// EventDTO is one stored event as the API returns it.
type EventDTO struct {
	Stream       string              `json:"stream"`
	Seq          uint64              `json:"seq"`
	Subject      string              `json:"subject"`
	StreamTime   time.Time           `json:"stream_time" doc:"When NATS stored the event"`
	Size         int                 `json:"size"`
	Headers      map[string][]string `json:"headers"`
	Data         string              `json:"data"`
	Encoding     string              `json:"encoding" enum:"utf8,base64"`
	ID           string              `json:"id,omitempty"`
	Type         string              `json:"type,omitempty"`
	Source       string              `json:"source,omitempty"`
	ContentType  string              `json:"content_type,omitempty"`
	EventTime    *time.Time          `json:"event_time,omitempty" doc:"ce-time"`
	Untyped      bool                `json:"untyped"`
	SubjectValid bool                `json:"subject_valid" doc:"Subject follows evt.<domain>.<entity>.<action>[.<key>]"`
	Blob         *envelope.BlobRef   `json:"blob,omitempty"`
}

func toDTO(m query.Message) EventDTO {
	h := nats.Header(m.Headers)
	e := envelope.Decode(m.Subject, h, m.Data)
	d := EventDTO{
		Stream: m.Stream, Seq: m.Seq, Subject: m.Subject, StreamTime: m.Time, Size: m.Size, Headers: m.Headers,
		ID: e.ID, Type: e.Type, Source: e.Source, ContentType: e.ContentType, Untyped: e.Untyped(), Blob: e.Blob,
		SubjectValid: envelope.ValidateSubject(m.Subject) == nil,
	}
	if d.Headers == nil {
		d.Headers = map[string][]string{}
	}
	if !e.Time.IsZero() {
		t := e.Time
		d.EventTime = &t
	}
	if utf8.Valid(m.Data) {
		d.Data, d.Encoding = string(m.Data), "utf8"
	} else {
		d.Data, d.Encoding = base64.StdEncoding.EncodeToString(m.Data), "base64"
	}
	return d
}
