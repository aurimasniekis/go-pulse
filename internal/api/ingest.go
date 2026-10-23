package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/nats-io/nats.go"

	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/query"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/tail"
	"github.com/aurimasniekis/go-pulse/internal/tokens"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// MaxRequestTimeout bounds the HTTP request bridge (PL-ING-004).
const MaxRequestTimeout = 30 * time.Second

func unauthorized() error {
	return huma.ErrorWithHeaders(huma.Error401Unauthorized("invalid or missing bearer token"),
		http.Header{"WWW-Authenticate": {`Bearer realm="pulse"`}})
}

func bearerOf(h string) string {
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// authorize authenticates the bearer token and checks one grant (PL-ING-007). Failed authentication is reported to
// OnAuthFailure (the ban detector, M8); a valid token without the grant gets 403.
func (d *Deps) authorize(ctx context.Context, authz string, action natsauth.Action, subject string) (*tokens.Token, error) {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	tok, err := d.Tokens.Authenticate(ctx, bearerOf(authz), ip)
	if err != nil {
		if d.OnAuthFailure != nil {
			d.OnAuthFailure(ip, "http_token")
		}
		return nil, unauthorized()
	}
	if subject != "" && !natsauth.Allows(tok.Grants, action, subject) {
		return nil, huma.Error403Forbidden("token has no " + string(action) + " grant for " + subject)
	}
	return tok, nil
}

// PublishOutput is the acknowledgement of one published event.
type PublishOutput struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	ID        string `json:"id"`
	Duplicate bool   `json:"duplicate"`
}

// BatchEvent is one event of a batch publish.
type BatchEvent struct {
	Subject     string          `json:"subject" maxLength:"256"`
	Type        string          `json:"type,omitempty" maxLength:"256"`
	Source      string          `json:"source,omitempty" maxLength:"256"`
	ID          string          `json:"id,omitempty" maxLength:"128" doc:"Idempotency key (Nats-Msg-Id)"`
	ContentType string          `json:"content_type,omitempty" maxLength:"128"`
	Data        json.RawMessage `json:"data,omitempty" doc:"Any JSON value; a JSON string is stored as text"`
	DataBase64  string          `json:"data_base64,omitempty" doc:"Binary payload; exclusive with data"`
}

func (b BatchEvent) payload() ([]byte, string, error) {
	if len(b.Data) > 0 && b.DataBase64 != "" {
		return nil, "", errors.New("data and data_base64 are exclusive")
	}
	if b.DataBase64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b.DataBase64)
		return raw, envelope.ContentBinary, err
	}
	if len(b.Data) == 0 || string(b.Data) == "null" {
		return nil, "", nil
	}
	var s string
	if json.Unmarshal(b.Data, &s) == nil {
		return []byte(s), envelope.ContentText, nil
	}
	return b.Data, envelope.ContentJSON, nil
}

func registerIngest(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "publish", Method: http.MethodPost, Path: "/publish/{subject}", Summary: "Publish one event",
		Description: "The request body is the payload, stored as-is. Envelope fields come from headers.", Tags: []string{"ingest"},
		DefaultStatus: http.StatusCreated, Security: []map[string][]string{{"bearer": {}}}},
		func(ctx context.Context, in *struct {
			Authorization  string `header:"Authorization"`
			Subject        string `path:"subject" maxLength:"256"`
			Type           string `header:"Ce-Type" maxLength:"256"`
			Source         string `header:"Ce-Source" maxLength:"256"`
			IdempotencyKey string `header:"Idempotency-Key" maxLength:"128" doc:"Stored as Nats-Msg-Id; retries within 2 minutes are de-duplicated"`
			ContentType    string `header:"Content-Type"`
			RawBody        []byte
		}) (*struct{ Body PublishOutput }, error) {
			tok, err := d.authorize(ctx, in.Authorization, natsauth.ActPublish, in.Subject)
			if err != nil {
				return nil, err
			}
			out, err := d.publishOne(ctx, tok, BatchEvent{Subject: in.Subject, Type: in.Type, Source: in.Source, ID: in.IdempotencyKey,
				ContentType: in.ContentType}, in.RawBody)
			if err != nil {
				return nil, err
			}
			return &struct{ Body PublishOutput }{*out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "publish-batch", Method: http.MethodPost, Path: "/publish", Summary: "Publish up to 100 events",
		Description: "All events are authorized before any is published.", Tags: []string{"ingest"}, Security: []map[string][]string{{"bearer": {}}}},
		func(ctx context.Context, in *struct {
			Authorization string `header:"Authorization"`
			Body          struct {
				Events []BatchEvent `json:"events" minItems:"1" maxItems:"100"`
			}
		}) (*struct {
			Body struct {
				Results []PublishOutput `json:"results"`
			}
		}, error) {
			tok, err := d.authorize(ctx, in.Authorization, "", "")
			if err != nil {
				return nil, err
			}
			payloads := make([][]byte, len(in.Body.Events))
			for i, e := range in.Body.Events {
				if !natsauth.Allows(tok.Grants, natsauth.ActPublish, e.Subject) {
					return nil, huma.Error403Forbidden("token has no publish grant for " + e.Subject)
				}
				p, ct, err := e.payload()
				if err != nil {
					return nil, huma.Error422UnprocessableEntity("event " + e.Subject + ": " + err.Error())
				}
				if in.Body.Events[i].ContentType == "" {
					in.Body.Events[i].ContentType = ct
				}
				payloads[i] = p
			}
			out := &struct {
				Body struct {
					Results []PublishOutput `json:"results"`
				}
			}{}
			out.Body.Results = make([]PublishOutput, 0, len(payloads))
			for i, e := range in.Body.Events {
				r, err := d.publishOne(ctx, tok, e, payloads[i])
				if err != nil {
					return nil, err
				}
				out.Body.Results = append(out.Body.Results, *r)
			}
			return out, nil
		})

	huma.Register(api, huma.Operation{OperationID: "request", Method: http.MethodPost, Path: "/request/{subject}", Summary: "Request/reply bridge",
		Description: "Sends the body as a NATS request and returns the reply body. 503 when nobody serves the subject, 504 on timeout.",
		Tags:        []string{"ingest"}, Security: []map[string][]string{{"bearer": {}}}},
		func(ctx context.Context, in *struct {
			Authorization string `header:"Authorization"`
			Subject       string `path:"subject" maxLength:"256"`
			TimeoutMs     int    `query:"timeout_ms" default:"5000" minimum:"100" maximum:"30000"`
			ContentType   string `header:"Content-Type"`
			RawBody       []byte
		}) (*struct {
			ContentType string `header:"Content-Type"`
			Body        []byte
		}, error) {
			if _, err := d.authorize(ctx, in.Authorization, natsauth.ActRequest, in.Subject); err != nil {
				return nil, err
			}
			m := nats.NewMsg(in.Subject)
			m.Data = in.RawBody
			if in.ContentType != "" {
				m.Header.Set(envelope.HeaderContentType, in.ContentType)
			}
			rctx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutMs)*time.Millisecond)
			defer cancel()
			reply, err := d.NC.RequestMsgWithContext(rctx, m)
			switch {
			case errors.Is(err, nats.ErrNoResponders):
				return nil, huma.Error503ServiceUnavailable("no responders for " + in.Subject)
			case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
				return nil, huma.Error504GatewayTimeout("no reply within the timeout")
			case err != nil:
				return nil, d.internalErr("request", err)
			}
			ct := reply.Header.Get(envelope.HeaderContentType)
			if ct == "" {
				ct = envelope.ContentBinary
			}
			return &struct {
				ContentType string `header:"Content-Type"`
				Body        []byte
			}{ct, reply.Data}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "replay", Method: http.MethodGet, Path: "/events", Summary: "Replay events",
		Description: "Oldest first from `since` or `seq`, paged with `cursor`. Needs a subscribe grant covering `subject`.",
		Tags:        []string{"ingest"}, Security: []map[string][]string{{"bearer": {}}}},
		func(ctx context.Context, in *struct {
			Authorization string    `header:"Authorization"`
			Subject       string    `query:"subject" required:"true" maxLength:"256"`
			Since         time.Time `query:"since"`
			Seq           uint64    `query:"seq"`
			Limit         int       `query:"limit" default:"100" minimum:"1" maximum:"500"`
			Cursor        string    `query:"cursor" maxLength:"512"`
		}) (*struct{ Body EventsPage }, error) {
			if _, err := d.authorize(ctx, in.Authorization, natsauth.ActSubscribe, in.Subject); err != nil {
				return nil, err
			}
			r := query.Request{Stream: streams.Events, Subjects: []string{in.Subject}, Dir: query.Forward, StartSeq: in.Seq,
				StartTime: in.Since, Limit: in.Limit, Cursor: in.Cursor}
			res, err := d.Query.Run(ctx, r)
			if err != nil {
				return nil, mapQueryErr(d, "replay", err)
			}
			p := EventsPage{Events: make([]EventDTO, 0, len(res.Messages)), Next: res.Next, Scanned: res.Scanned,
				ScannedBytes: res.ScannedBytes, Partial: res.Partial, Reason: res.Reason, FirstSeq: res.FirstSeq, LastSeq: res.LastSeq}
			for _, m := range res.Messages {
				p.Events = append(p.Events, toDTO(m))
			}
			return &struct{ Body EventsPage }{p}, nil
		})

	// The SSE handler commits a 200 before its body runs, so authentication happens in an operation middleware that can
	// still answer 401/403.
	tailAuth := func(ctx huma.Context, next func(huma.Context)) {
		r, _ := humago.Unwrap(ctx)
		rctx := context.WithValue(ctx.Context(), clientIPKey{}, d.clientIP(r))
		subject := ctx.Query("subject")
		if subject == "" {
			subject = "evt.>"
		}
		if _, err := d.authorize(rctx, ctx.Header("Authorization"), natsauth.ActSubscribe, subject); err != nil {
			var se huma.StatusError
			if errors.As(err, &se) {
				var hdr http.Header
				var he huma.HeadersError
				if errors.As(err, &he) {
					hdr = he.GetHeaders()
				}
				for k, vs := range hdr {
					for _, v := range vs {
						ctx.AppendHeader(k, v)
					}
				}
				_ = huma.WriteErr(api, ctx, se.GetStatus(), se.Error())
			}
			return
		}
		next(ctx)
	}
	sse.Register(api, huma.Operation{OperationID: "tail", Method: http.MethodGet, Path: "/tail", Summary: "Live events (SSE)",
		Description: "Streams `event` messages (id = stream sequence; resume with Last-Event-ID). Needs a subscribe grant covering `subject`.",
		Tags:        []string{"ingest"}, Security: []map[string][]string{{"bearer": {}}}, Middlewares: huma.Middlewares{tailAuth}},
		map[string]any{"event": EventDTO{}, "notice": tail.Notice{}},
		func(ctx context.Context, in *struct {
			Authorization string    `header:"Authorization"`
			Subject       string    `query:"subject" maxLength:"256" doc:"Filter; default evt.>"`
			Since         time.Time `query:"since"`
			LastEventID   uint64    `header:"Last-Event-ID"`
		}, send sse.Sender) {
			subj := in.Subject
			if subj == "" {
				subj = "evt.>"
			}
			r := tail.Request{Stream: streams.Events, Subjects: []string{subj}, Since: in.Since}
			if in.LastEventID > 0 {
				r.FromSeq, r.Since = in.LastEventID+1, time.Time{}
			}
			streamTail(ctx, d, r, send)
		})
}

// streamTail runs one tail session over SSE until the client leaves.
func streamTail(ctx context.Context, d *Deps, r tail.Request, send sse.Sender) {
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
}

func (d *Deps) publishOne(ctx context.Context, tok *tokens.Token, e BatchEvent, data []byte) (*PublishOutput, error) {
	if len(data) > streams.MaxMsgSize-8192 {
		return nil, huma.Error413RequestEntityTooLarge("payload above ~1 MiB; store it as a blob and publish a reference")
	}
	src := e.Source
	if src == "" {
		src = "token:" + tok.Name
	}
	msg, err := envelope.Encode(envelope.Event{Subject: e.Subject, ID: e.ID, Type: e.Type, Source: src, ContentType: e.ContentType, Data: data})
	if errors.Is(err, envelope.ErrSubject) {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	if err != nil {
		return nil, d.internalErr("publish", err)
	}
	ack, err := d.JS.PublishMsg(ctx, msg)
	if errors.Is(err, nats.ErrMaxPayload) {
		return nil, huma.Error413RequestEntityTooLarge("payload exceeds the NATS max_payload")
	}
	if err != nil {
		return nil, d.internalErr("publish", err)
	}
	return &PublishOutput{Stream: ack.Stream, Seq: ack.Sequence, ID: msg.Header.Get(envelope.HeaderID), Duplicate: ack.Duplicate}, nil
}
