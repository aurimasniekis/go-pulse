package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/query"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/tail"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// PublishInput is an operator-published event (dashboard "publish test event", e2e seeding).
type PublishInput struct {
	Subject     string            `json:"subject" maxLength:"256" doc:"evt.<domain>.<entity>.<action>[.<key>]"`
	Type        string            `json:"type,omitempty" maxLength:"256" doc:"ce-type; empty publishes an untyped event"`
	Source      string            `json:"source,omitempty" maxLength:"256" doc:"ce-source; default dashboard"`
	ID          string            `json:"id,omitempty" maxLength:"128" doc:"Nats-Msg-Id; default a new ULID"`
	ContentType string            `json:"content_type,omitempty" maxLength:"128"`
	Data        string            `json:"data,omitempty" maxLength:"1048576" doc:"UTF-8 payload"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// PublishResult is the stream acknowledgement.
type PublishResult struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	ID        string `json:"id"`
	Duplicate bool   `json:"duplicate"`
}

// EventsInput is an explorer page request (PL-EXP-001..006).
type EventsInput struct {
	Stream    string    `query:"stream" default:"EVENTS" pattern:"^[A-Za-z0-9_-]{1,64}$"`
	Subject   []string  `query:"subject,explode" doc:"Subject filter; repeat for several (wildcards allowed)"`
	Dir       string    `query:"dir" enum:"forward,backward" default:"backward" doc:"backward = newest first"`
	StartSeq  uint64    `query:"start_seq" doc:"forward: first sequence; backward: last sequence (inclusive)"`
	StartTime time.Time `query:"start_time" doc:"forward only (RFC 3339)"`
	Limit     int       `query:"limit" default:"50" minimum:"1" maximum:"500"`
	Type      string    `query:"type" doc:"Exact ce-type, or (untyped)"`
	Header    []string  `query:"header,explode" doc:"key=value, repeatable"`
	Contains  string    `query:"contains" maxLength:"256" doc:"Payload substring"`
	JSONPath  string    `query:"json_path" maxLength:"256" doc:"Dot path into a JSON payload"`
	JSONValue string    `query:"json_value" maxLength:"256"`
	Cursor    string    `query:"cursor" maxLength:"512" doc:"Opaque cursor from a previous page"`
}

func (in *EventsInput) toRequest() (query.Request, error) {
	r := query.Request{Stream: in.Stream, Subjects: in.Subject, Dir: query.Direction(in.Dir), StartSeq: in.StartSeq,
		StartTime: in.StartTime, Limit: in.Limit, Type: in.Type, Contains: in.Contains, JSONPath: in.JSONPath,
		JSONValue: in.JSONValue, Cursor: in.Cursor}
	if len(in.Header) > 0 {
		r.Headers = map[string]string{}
		for _, h := range in.Header {
			k, v, ok := strings.Cut(h, "=")
			if !ok || k == "" {
				return r, huma.Error400BadRequest("header filters are key=value")
			}
			r.Headers[k] = v
		}
	}
	return r, nil
}

// EventsPage is an explorer page.
type EventsPage struct {
	Events       []EventDTO `json:"events"`
	Next         string     `json:"next,omitempty" doc:"Cursor for the next page; absent at the end"`
	Scanned      int        `json:"scanned"`
	ScannedBytes int64      `json:"scanned_bytes"`
	Partial      bool       `json:"partial" doc:"The scan budget ran out before the page filled; continue with next"`
	Reason       string     `json:"reason,omitempty" enum:"msgs,bytes,time"`
	FirstSeq     uint64     `json:"first_seq"`
	LastSeq      uint64     `json:"last_seq"`
}

// TailInput opens a live tail (PL-EXP-007).
type TailInput struct {
	Stream      string    `query:"stream" default:"EVENTS" pattern:"^[A-Za-z0-9_-]{1,64}$"`
	Subject     []string  `query:"subject,explode"`
	Since       time.Time `query:"since" doc:"Replay from this time before going live"`
	LastEventID uint64    `header:"Last-Event-ID" doc:"Resume after this stream sequence"`
}

func registerEvents(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "publish-event", Method: http.MethodPost, Path: "/events", Summary: "Publish an event",
		Description: "Publishes one event into EVENTS as the operator (audited).", Tags: []string{"events"}, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct{ Body PublishInput }) (*struct{ Body PublishResult }, error) {
			b := in.Body
			src := b.Source
			if src == "" {
				src = "dashboard"
			}
			ev := envelope.Event{Subject: b.Subject, ID: b.ID, Type: b.Type, Source: src, ContentType: b.ContentType, Data: []byte(b.Data)}
			for k, v := range b.Headers {
				if ev.Extra == nil {
					ev.Extra = map[string][]string{}
				}
				ev.Extra[k] = []string{v}
			}
			msg, err := envelope.Encode(ev)
			if errors.Is(err, envelope.ErrSubject) {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			if err != nil {
				return nil, d.internalErr("publish", err)
			}
			ack, err := d.JS.PublishMsg(ctx, msg)
			if err != nil {
				return nil, mapQueryErr(d, "publish", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "event.publish", b.Subject, audit.OK, map[string]any{"seq": ack.Sequence, "type": b.Type})
			return &struct{ Body PublishResult }{PublishResult{Stream: ack.Stream, Seq: ack.Sequence, ID: msg.Header.Get(envelope.HeaderID),
				Duplicate: ack.Duplicate}}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "query-events", Method: http.MethodGet, Path: "/events", Summary: "Explore events", Tags: []string{"events"}},
		func(ctx context.Context, in *EventsInput) (*struct{ Body EventsPage }, error) {
			r, err := in.toRequest()
			if err != nil {
				return nil, err
			}
			res, err := d.Query.Run(ctx, r)
			if err != nil {
				return nil, mapQueryErr(d, "query-events", err)
			}
			p := EventsPage{Events: make([]EventDTO, 0, len(res.Messages)), Next: res.Next, Scanned: res.Scanned,
				ScannedBytes: res.ScannedBytes, Partial: res.Partial, Reason: res.Reason, FirstSeq: res.FirstSeq, LastSeq: res.LastSeq}
			for _, m := range res.Messages {
				p.Events = append(p.Events, toDTO(m))
			}
			return &struct{ Body EventsPage }{p}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "get-event", Method: http.MethodGet, Path: "/events/{stream}/{seq}", Summary: "Get one event by sequence", Tags: []string{"events"}},
		func(ctx context.Context, in *struct {
			Stream string `path:"stream" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Seq    uint64 `path:"seq" minimum:"1"`
		}) (*struct{ Body EventDTO }, error) {
			s, err := d.JS.Stream(ctx, in.Stream)
			if err != nil {
				return nil, mapQueryErr(d, "get-event", err)
			}
			m, err := s.GetMsg(ctx, in.Seq)
			if err != nil {
				return nil, mapQueryErr(d, "get-event", err)
			}
			size := len(m.Subject) + len(m.Data)
			return &struct{ Body EventDTO }{toDTO(query.Message{Stream: in.Stream, Seq: m.Sequence, Subject: m.Subject, Time: m.Time.UTC(),
				Headers: m.Header, Data: m.Data, Size: size})}, nil
		})

	sse.Register(api, huma.Operation{OperationID: "tail-events", Method: http.MethodGet, Path: "/tail", Summary: "Live tail (SSE)",
		Description: "Streams `event` messages (id = stream sequence) and `notice` messages when events were dropped by the per-session " +
			"rate limit. A comment keepalive is sent every 20 s.", Tags: []string{"events"}},
		map[string]any{"event": EventDTO{}, "notice": tail.Notice{}},
		func(ctx context.Context, in *TailInput, send sse.Sender) {
			stream := in.Stream
			if stream == "" {
				stream = streams.Events
			}
			r := tail.Request{Stream: stream, Subjects: in.Subject, Since: in.Since}
			if in.LastEventID > 0 {
				r.FromSeq, r.Since = in.LastEventID+1, time.Time{}
			}
			s, err := d.Tail.Open(ctx, r)
			if err != nil {
				_ = send.Data(tail.Notice{Reason: "error: " + err.Error()})
				return
			}
			defer s.Close()
			ka := time.NewTicker(tail.Keepalive)
			defer ka.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ka.C:
					s.Flush()
					if send(sse.Message{Comment: "keepalive"}) != nil {
						return
					}
				case it := <-s.Items():
					var err error
					if it.Msg != nil {
						err = send(sse.Message{ID: int(it.Msg.Seq), Data: toDTO(*it.Msg)}) //nolint:gosec // sequences fit int on 64-bit
					} else {
						err = send.Data(*it.Notice)
					}
					if err != nil {
						return
					}
				}
			}
		})
}
