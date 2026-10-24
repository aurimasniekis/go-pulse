package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/streams"
)

// StreamState mirrors JetStream stream state.
type StreamState struct {
	Msgs        uint64    `json:"msgs"`
	Bytes       uint64    `json:"bytes"`
	FirstSeq    uint64    `json:"first_seq"`
	FirstTime   time.Time `json:"first_time"`
	LastSeq     uint64    `json:"last_seq"`
	LastTime    time.Time `json:"last_time"`
	Consumers   int       `json:"consumers"`
	NumSubjects uint64    `json:"num_subjects"`
	NumDeleted  int       `json:"num_deleted"`
}

// StreamDTO describes a stream.
type StreamDTO struct {
	Name              string      `json:"name"`
	Kind              string      `json:"kind" enum:"stream,kv,object" doc:"KV buckets and object stores are streams too"`
	Managed           bool        `json:"managed" doc:"Owned by pulsed; cannot be edited or deleted here"`
	Description       string      `json:"description"`
	Subjects          []string    `json:"subjects"`
	Storage           string      `json:"storage"`
	Retention         string      `json:"retention"`
	Discard           string      `json:"discard"`
	MaxBytes          int64       `json:"max_bytes"`
	MaxMsgs           int64       `json:"max_msgs"`
	MaxAgeSeconds     int64       `json:"max_age_seconds"`
	MaxMsgSize        int32       `json:"max_msg_size"`
	Compression       string      `json:"compression"`
	DuplicatesSeconds int64       `json:"duplicates_seconds"`
	AllowDirect       bool        `json:"allow_direct"`
	Created           time.Time   `json:"created"`
	State             StreamState `json:"state"`
}

func streamKind(name string) string {
	switch {
	case strings.HasPrefix(name, "KV_"):
		return "kv"
	case strings.HasPrefix(name, "OBJ_"):
		return "object"
	}
	return "stream"
}

func isManaged(name string) bool {
	return name == streams.Events || name == "OBJ_"+natsauth.BlobsBucket
}

func streamDTO(i *jetstream.StreamInfo) StreamDTO {
	c := i.Config
	return StreamDTO{
		Name: c.Name, Kind: streamKind(c.Name), Managed: isManaged(c.Name), Description: c.Description, Subjects: nonNilS(c.Subjects),
		Storage: c.Storage.String(), Retention: c.Retention.String(), Discard: c.Discard.String(), MaxBytes: c.MaxBytes,
		MaxMsgs: c.MaxMsgs, MaxAgeSeconds: int64(c.MaxAge / time.Second), MaxMsgSize: c.MaxMsgSize,
		Compression: c.Compression.String(), DuplicatesSeconds: int64(c.Duplicates / time.Second), AllowDirect: c.AllowDirect,
		Created: i.Created,
		State: StreamState{Msgs: i.State.Msgs, Bytes: i.State.Bytes, FirstSeq: i.State.FirstSeq, FirstTime: i.State.FirstTime,
			LastSeq: i.State.LastSeq, LastTime: i.State.LastTime, Consumers: i.State.Consumers, NumSubjects: i.State.NumSubjects,
			NumDeleted: i.State.NumDeleted},
	}
}

func nonNilS(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// StreamInput creates or updates a user stream (PL-BUS-005).
type StreamInput struct {
	Name              string   `json:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
	Description       string   `json:"description,omitempty" maxLength:"512"`
	Subjects          []string `json:"subjects" minItems:"1" maxItems:"32"`
	Storage           string   `json:"storage,omitempty" enum:"file,memory" default:"file"`
	MaxBytes          int64    `json:"max_bytes,omitempty" minimum:"-1"`
	MaxMsgs           int64    `json:"max_msgs,omitempty" minimum:"-1"`
	MaxAgeSeconds     int64    `json:"max_age_seconds,omitempty" minimum:"0"`
	MaxMsgSize        int32    `json:"max_msg_size,omitempty" minimum:"-1" maximum:"1048576"`
	Discard           string   `json:"discard,omitempty" enum:"old,new" default:"old"`
	Compression       bool     `json:"compression,omitempty"`
	DuplicatesSeconds int64    `json:"duplicates_seconds,omitempty" minimum:"0" maximum:"86400"`
	AllowDirect       bool     `json:"allow_direct,omitempty"`
}

var reservedRoots = []string{"evt.>", "_sys.>", "$JS.>", "$KV.>", "$O.>", "$SYS.>", "_INBOX.>"}

// SubjectsCollide reports whether two subject patterns can match a common subject.
func SubjectsCollide(a, b string) bool {
	at, bt := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; ; i++ {
		switch {
		case i < len(at) && at[i] == ">", i < len(bt) && bt[i] == ">":
			return i < len(at) && i < len(bt) || (i < len(at) && at[i] == ">" && i < len(bt)) || (i < len(bt) && bt[i] == ">" && i < len(at))
		case i == len(at) || i == len(bt):
			return len(at) == len(bt)
		case at[i] == "*" || bt[i] == "*" || at[i] == bt[i]:
		default:
			return false
		}
	}
}

var subjTokenRe = regexp.MustCompile(`^([A-Za-z0-9_-]+|\*|>)$`)

func (in *StreamInput) toConfig() (jetstream.StreamConfig, error) {
	if isManaged(in.Name) || strings.HasPrefix(in.Name, "KV_") || strings.HasPrefix(in.Name, "OBJ_") {
		return jetstream.StreamConfig{}, fmt.Errorf("stream name %q is reserved", in.Name)
	}
	for _, s := range in.Subjects {
		toks := strings.Split(s, ".")
		for i, t := range toks {
			if !subjTokenRe.MatchString(t) || (t == ">" && i != len(toks)-1) {
				return jetstream.StreamConfig{}, fmt.Errorf("invalid subject %q", s)
			}
		}
		for _, r := range reservedRoots {
			if SubjectsCollide(s, r) {
				return jetstream.StreamConfig{}, fmt.Errorf("subject %q overlaps reserved %q", s, r)
			}
		}
	}
	c := jetstream.StreamConfig{
		Name: in.Name, Description: in.Description, Subjects: in.Subjects, Storage: jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, MaxBytes: -1, MaxMsgs: -1, MaxMsgSize: -1, Discard: jetstream.DiscardOld, Replicas: 1,
		MaxAge: time.Duration(in.MaxAgeSeconds) * time.Second, AllowDirect: in.AllowDirect,
		Duplicates: time.Duration(in.DuplicatesSeconds) * time.Second,
	}
	if in.Storage == "memory" {
		c.Storage = jetstream.MemoryStorage
	}
	if in.MaxBytes != 0 {
		c.MaxBytes = in.MaxBytes
	}
	if in.MaxMsgs != 0 {
		c.MaxMsgs = in.MaxMsgs
	}
	if in.MaxMsgSize != 0 {
		c.MaxMsgSize = in.MaxMsgSize
	}
	if in.Discard == "new" {
		c.Discard = jetstream.DiscardNew
	}
	if in.Compression {
		c.Compression = jetstream.S2Compression
	}
	return c, nil
}

// ConsumerDTO describes a consumer (PL-BUS-006).
type ConsumerDTO struct {
	Name           string     `json:"name"`
	Durable        bool       `json:"durable"`
	Description    string     `json:"description"`
	FilterSubjects []string   `json:"filter_subjects"`
	DeliverPolicy  string     `json:"deliver_policy" enum:"all,new,last,last_per_subject,by_start_sequence,by_start_time"`
	OptStartSeq    uint64     `json:"opt_start_seq,omitempty"`
	OptStartTime   *time.Time `json:"opt_start_time,omitempty"`
	AckPolicy      string     `json:"ack_policy"`
	AckWaitSeconds int64      `json:"ack_wait_seconds"`
	MaxDeliver     int        `json:"max_deliver" doc:"-1: unlimited"`
	MaxAckPending  int        `json:"max_ack_pending" doc:"-1: unlimited"`
	Created        time.Time  `json:"created"`
	NumPending     uint64     `json:"num_pending"`
	NumAckPending  int        `json:"num_ack_pending"`
	NumRedelivered int        `json:"num_redelivered"`
	NumWaiting     int        `json:"num_waiting"`
	DeliveredSeq   uint64     `json:"delivered_stream_seq"`
	AckFloorSeq    uint64     `json:"ack_floor_stream_seq"`
	LastActive     *time.Time `json:"last_active,omitempty"`
	Paused         bool       `json:"paused"`
	Internal       bool       `json:"internal" doc:"Created by pulsed (rollups, tail, explorer, webhooks)"`
}

const internalConsumerPrefix = "pulsed:"

func consumerDTO(i *jetstream.ConsumerInfo) ConsumerDTO {
	c := i.Config
	f := c.FilterSubjects
	if c.FilterSubject != "" {
		f = []string{c.FilterSubject}
	}
	d := ConsumerDTO{Name: i.Name, Durable: c.Durable != "", Description: c.Description, FilterSubjects: nonNilS(f),
		DeliverPolicy: c.DeliverPolicy.String(), OptStartSeq: c.OptStartSeq, OptStartTime: c.OptStartTime,
		AckPolicy: c.AckPolicy.String(), AckWaitSeconds: int64(c.AckWait / time.Second), MaxDeliver: c.MaxDeliver,
		MaxAckPending: c.MaxAckPending, Created: i.Created, NumPending: i.NumPending,
		NumAckPending: i.NumAckPending, NumRedelivered: i.NumRedelivered, NumWaiting: i.NumWaiting,
		DeliveredSeq: i.Delivered.Stream, AckFloorSeq: i.AckFloor.Stream, Paused: i.Paused,
		Internal: strings.HasPrefix(c.Description, internalConsumerPrefix)}
	if i.Delivered.Last != nil {
		d.LastActive = i.Delivered.Last
	}
	return d
}

// ConsumerInput creates or updates a durable pull consumer with explicit acks.
type ConsumerInput struct {
	Description    string     `json:"description,omitempty" maxLength:"512"`
	FilterSubjects []string   `json:"filter_subjects" minItems:"1" maxItems:"16"`
	DeliverPolicy  string     `json:"deliver_policy,omitempty" enum:"all,new,last,last_per_subject,by_start_sequence,by_start_time" default:"all"`
	OptStartSeq    uint64     `json:"opt_start_seq,omitempty" doc:"Required with by_start_sequence"`
	OptStartTime   *time.Time `json:"opt_start_time,omitempty" doc:"Required with by_start_time (RFC 3339)"`
	AckWaitSeconds int64      `json:"ack_wait_seconds,omitempty" minimum:"0" maximum:"2147483647" doc:"0: server default (30 s)"`
	MaxDeliver     int        `json:"max_deliver,omitempty" minimum:"-1" maximum:"2147483647" doc:"0 or -1: unlimited"`
	MaxAckPending  int        `json:"max_ack_pending,omitempty" minimum:"-1" maximum:"2147483647" doc:"0: server default; -1: unlimited"`
}

var deliverPolicies = map[string]jetstream.DeliverPolicy{
	"": jetstream.DeliverAllPolicy, "all": jetstream.DeliverAllPolicy, "new": jetstream.DeliverNewPolicy,
	"last": jetstream.DeliverLastPolicy, "last_per_subject": jetstream.DeliverLastPerSubjectPolicy,
	"by_start_sequence": jetstream.DeliverByStartSequencePolicy, "by_start_time": jetstream.DeliverByStartTimePolicy,
}

// validSubjectFilter reports whether s is a NATS subject, optionally with * and > wildcards (> last).
func validSubjectFilter(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	toks := strings.Split(s, ".")
	for i, t := range toks {
		if t == "" || strings.ContainsFunc(t, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return false
		}
		if (strings.Contains(t, "*") && t != "*") || (strings.Contains(t, ">") && (t != ">" || i != len(toks)-1)) {
			return false
		}
	}
	return true
}

func (in *ConsumerInput) toConfig(name string) (jetstream.ConsumerConfig, error) {
	if strings.HasPrefix(in.Description, internalConsumerPrefix) {
		return jetstream.ConsumerConfig{}, fmt.Errorf("descriptions starting with %q are reserved for pulsed", internalConsumerPrefix)
	}
	for _, f := range in.FilterSubjects {
		if !validSubjectFilter(f) {
			return jetstream.ConsumerConfig{}, fmt.Errorf("invalid filter subject %q", f)
		}
	}
	dp, ok := deliverPolicies[in.DeliverPolicy]
	if !ok {
		return jetstream.ConsumerConfig{}, fmt.Errorf("invalid deliver_policy %q", in.DeliverPolicy)
	}
	switch {
	case dp == jetstream.DeliverByStartSequencePolicy && in.OptStartSeq == 0:
		return jetstream.ConsumerConfig{}, errors.New("opt_start_seq is required with by_start_sequence")
	case dp != jetstream.DeliverByStartSequencePolicy && in.OptStartSeq != 0:
		return jetstream.ConsumerConfig{}, errors.New("opt_start_seq is only allowed with by_start_sequence")
	case dp == jetstream.DeliverByStartTimePolicy && (in.OptStartTime == nil || in.OptStartTime.IsZero()):
		return jetstream.ConsumerConfig{}, errors.New("opt_start_time is required with by_start_time")
	case dp != jetstream.DeliverByStartTimePolicy && in.OptStartTime != nil:
		return jetstream.ConsumerConfig{}, errors.New("opt_start_time is only allowed with by_start_time")
	}
	c := jetstream.ConsumerConfig{
		Durable: name, Description: in.Description, DeliverPolicy: dp, OptStartSeq: in.OptStartSeq,
		AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Duration(in.AckWaitSeconds) * time.Second,
		MaxDeliver: in.MaxDeliver, MaxAckPending: in.MaxAckPending, ReplayPolicy: jetstream.ReplayInstantPolicy,
	}
	if c.MaxDeliver == 0 {
		c.MaxDeliver = -1
	}
	if in.OptStartTime != nil {
		t := in.OptStartTime.UTC()
		c.OptStartTime = &t
	}
	if len(in.FilterSubjects) == 1 {
		c.FilterSubject = in.FilterSubjects[0]
	} else {
		c.FilterSubjects = in.FilterSubjects
	}
	return c, nil
}

type consumerPath struct {
	Name     string `path:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
	Consumer string `path:"consumer" pattern:"^[A-Za-z0-9_-]{1,128}$"`
}

type streamPath struct {
	Name string `path:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
}

func registerStreams(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "list-streams", Method: http.MethodGet, Path: "/streams", Summary: "List streams", Tags: []string{"streams"}},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []StreamDTO }, error) {
			out := &struct{ Body []StreamDTO }{Body: []StreamDTO{}}
			l := d.JS.ListStreams(ctx)
			for i := range l.Info() {
				out.Body = append(out.Body, streamDTO(i))
			}
			if err := l.Err(); err != nil {
				return nil, d.internalErr("list-streams", err)
			}
			sort.Slice(out.Body, func(i, j int) bool { return out.Body[i].Name < out.Body[j].Name })
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-stream", Method: http.MethodGet, Path: "/streams/{name}", Summary: "Get a stream", Tags: []string{"streams"}},
		func(ctx context.Context, in *streamPath) (*struct{ Body StreamDTO }, error) {
			s, err := d.JS.Stream(ctx, in.Name)
			if err != nil {
				return nil, mapQueryErr(d, "get-stream", err)
			}
			return &struct{ Body StreamDTO }{streamDTO(s.CachedInfo())}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-stream", Method: http.MethodPost, Path: "/streams", Summary: "Create a user stream",
		Tags: []string{"streams"}, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct{ Body StreamInput }) (*struct{ Body StreamDTO }, error) {
			cfg, err := in.Body.toConfig()
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			s, err := d.JS.CreateStream(ctx, cfg)
			if err != nil {
				_ = audit.Write(ctx, d.DB, d.actor(ctx), "stream.create", in.Body.Name, audit.Error, map[string]any{"error": err.Error()})
				return nil, streamWriteErr(err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "stream.create", in.Body.Name, audit.OK, map[string]any{"subjects": cfg.Subjects})
			return &struct{ Body StreamDTO }{streamDTO(s.CachedInfo())}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-stream", Method: http.MethodPut, Path: "/streams/{name}", Summary: "Update a user stream", Tags: []string{"streams"}},
		func(ctx context.Context, in *struct {
			Name string `path:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Body StreamInput
		}) (*struct{ Body StreamDTO }, error) {
			if in.Body.Name != in.Name {
				return nil, huma.Error422UnprocessableEntity("name in body must match the path")
			}
			cfg, err := in.Body.toConfig()
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			s, err := d.JS.UpdateStream(ctx, cfg)
			if err != nil {
				return nil, streamWriteErr(err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "stream.update", in.Name, audit.OK, map[string]any{"subjects": cfg.Subjects})
			return &struct{ Body StreamDTO }{streamDTO(s.CachedInfo())}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-stream", Method: http.MethodDelete, Path: "/streams/{name}", Summary: "Delete a user stream",
		Description: "Deletes the stream and all its messages. `confirm` must repeat the stream name.", Tags: []string{"streams"}, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Name    string `path:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Confirm string `query:"confirm" required:"true"`
		}) (*struct{}, error) {
			if isManaged(in.Name) {
				return nil, huma.Error409Conflict(in.Name + " is managed by pulsed and cannot be deleted")
			}
			if in.Confirm != in.Name {
				return nil, huma.Error422UnprocessableEntity("confirm must equal the stream name")
			}
			if err := d.JS.DeleteStream(ctx, in.Name); err != nil {
				return nil, mapQueryErr(d, "delete-stream", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "stream.delete", in.Name, audit.OK, nil)
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-consumers", Method: http.MethodGet, Path: "/streams/{name}/consumers", Summary: "List consumers", Tags: []string{"streams"}},
		func(ctx context.Context, in *streamPath) (*struct{ Body []ConsumerDTO }, error) {
			s, err := d.JS.Stream(ctx, in.Name)
			if err != nil {
				return nil, mapQueryErr(d, "list-consumers", err)
			}
			out := &struct{ Body []ConsumerDTO }{Body: []ConsumerDTO{}}
			l := s.ListConsumers(ctx)
			for i := range l.Info() {
				out.Body = append(out.Body, consumerDTO(i))
			}
			if err := l.Err(); err != nil {
				return nil, d.internalErr("list-consumers", err)
			}
			sort.Slice(out.Body, func(i, j int) bool { return out.Body[i].Name < out.Body[j].Name })
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-consumer", Method: http.MethodGet, Path: "/streams/{name}/consumers/{consumer}",
		Summary: "Get a consumer", Tags: []string{"streams"}},
		func(ctx context.Context, in *consumerPath) (*struct{ Body ConsumerDTO }, error) {
			c, err := d.JS.Consumer(ctx, in.Name, in.Consumer)
			if err != nil {
				return nil, consumerErr(d, "get-consumer", err)
			}
			return &struct{ Body ConsumerDTO }{consumerDTO(c.CachedInfo())}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "put-consumer", Method: http.MethodPut, Path: "/streams/{name}/consumers/{consumer}",
		Summary: "Create or update a durable pull consumer", Tags: []string{"streams"},
		Description: "The consumer name is the durable name; acks are explicit. NATS refuses changes to immutable settings such as the deliver policy."},
		func(ctx context.Context, in *struct {
			Name     string `path:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Consumer string `path:"consumer" pattern:"^[A-Za-z0-9_-]{1,128}$"`
			Body     ConsumerInput
		}) (*struct{ Body ConsumerDTO }, error) {
			cfg, err := in.Body.toConfig(in.Consumer)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			s, err := d.JS.Stream(ctx, in.Name)
			if err != nil {
				return nil, mapQueryErr(d, "put-consumer", err)
			}
			if streamKind(in.Name) != "stream" {
				return nil, huma.Error422UnprocessableEntity(in.Name + " backs a KV bucket or object store; consumers cannot be managed on it")
			}
			if subj := s.CachedInfo().Config.Subjects; len(subj) > 0 {
				for _, f := range in.Body.FilterSubjects {
					if !slices.ContainsFunc(subj, func(ss string) bool { return SubjectsCollide(f, ss) }) {
						return nil, huma.Error422UnprocessableEntity(fmt.Sprintf("filter subject %q matches none of the stream's subjects", f))
					}
				}
			}
			cur, err := s.Consumer(ctx, in.Consumer)
			created := errors.Is(err, jetstream.ErrConsumerNotFound)
			switch {
			case err == nil:
				if strings.HasPrefix(cur.CachedInfo().Config.Description, internalConsumerPrefix) {
					return nil, huma.Error409Conflict(in.Consumer + " is managed by pulsed")
				}
			case !errors.Is(err, jetstream.ErrConsumerNotFound):
				return nil, consumerErr(d, "put-consumer", err)
			}
			c, err := s.CreateOrUpdateConsumer(ctx, cfg)
			if err != nil {
				_ = audit.Write(ctx, d.DB, d.actor(ctx), "consumer.put", in.Name+"/"+in.Consumer, audit.Error, map[string]any{"error": err.Error()})
				return nil, streamWriteErr(err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "consumer.put", in.Name+"/"+in.Consumer, audit.OK,
				map[string]any{"filter_subjects": in.Body.FilterSubjects, "deliver_policy": cfg.DeliverPolicy.String(), "created": created})
			return &struct{ Body ConsumerDTO }{consumerDTO(c.CachedInfo())}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-consumer", Method: http.MethodDelete, Path: "/streams/{name}/consumers/{consumer}",
		Summary: "Delete a consumer", Tags: []string{"streams"}, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Name     string `path:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Consumer string `path:"consumer" pattern:"^[A-Za-z0-9_-]{1,128}$"`
			Confirm  string `query:"confirm" required:"true"`
		}) (*struct{}, error) {
			if in.Confirm != in.Consumer {
				return nil, huma.Error422UnprocessableEntity("confirm must equal the consumer name")
			}
			if err := d.JS.DeleteConsumer(ctx, in.Name, in.Consumer); err != nil {
				if errors.Is(err, jetstream.ErrConsumerNotFound) {
					return nil, huma.Error404NotFound("consumer not found")
				}
				return nil, mapQueryErr(d, "delete-consumer", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "consumer.delete", in.Name+"/"+in.Consumer, audit.OK, nil)
			return &struct{}{}, nil
		})
}

func consumerErr(d *Deps, op string, err error) error {
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return huma.Error404NotFound("consumer not found")
	}
	return mapQueryErr(d, op, err)
}

func streamWriteErr(err error) error {
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		return huma.NewError(http.StatusUnprocessableEntity, apiErr.Description)
	}
	if errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		return huma.Error409Conflict("stream exists")
	}
	return huma.Error500InternalServerError("internal error")
}
