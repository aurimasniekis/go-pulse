package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"
)

type connFlags struct {
	context string
	url     string
	creds   string
	ca      string
	source  string
}

func (f *connFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.context, "context", "", "context name (default: current)")
	cmd.Flags().StringVar(&f.url, "url", "", "NATS URL (overrides the context)")
	cmd.Flags().StringVar(&f.creds, "creds", "", "creds file (overrides the context)")
	cmd.Flags().StringVar(&f.ca, "ca", "", "extra CA PEM (overrides the context)")
	cmd.Flags().StringVar(&f.source, "source", "pulse-cli", "ce-source for published events")
}

func (f *connFlags) connect() (*pulse.Client, error) {
	cc, err := activeContext(f.context)
	if err != nil {
		return nil, err
	}
	if f.url != "" {
		cc.URL = f.url
	}
	if f.creds != "" {
		cc.Creds = f.creds
	}
	if f.ca != "" {
		cc.CA = f.ca
	}
	if cc.URL == "" || cc.Creds == "" {
		return nil, errors.New("no connection configured: run `pulse context add` or pass --url and --creds")
	}
	opts := []pulse.Option{pulse.WithCredsFile(cc.Creds), pulse.WithSource(f.source), pulse.WithName("pulse-cli")}
	if cc.CA != "" {
		opts = append(opts, pulse.WithCAFile(cc.CA))
	}
	return pulse.Connect(cc.URL, opts...)
}

func newPubCmd() *cobra.Command {
	var cf connFlags
	var typ, id, contentType string
	var headers []string
	cmd := &cobra.Command{
		Use:   "pub <subject> [data|-]",
		Short: "Publish an event (data from the argument, or stdin with -)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			if len(args) == 2 {
				if args[1] == "-" {
					b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20+1))
					if err != nil {
						return err
					}
					data = b
				} else {
					data = []byte(args[1])
				}
			}
			if contentType == "" {
				if json.Valid(data) && len(data) > 0 {
					contentType = "application/json"
				} else if utf8.Valid(data) {
					contentType = "text/plain; charset=utf-8"
				} else {
					contentType = "application/octet-stream"
				}
			}
			opts := []pulse.PublishOption{pulse.WithContentType(contentType)}
			if id != "" {
				opts = append(opts, pulse.WithID(id))
			}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, "=")
				if !ok {
					return fmt.Errorf("header %q must be key=value", h)
				}
				opts = append(opts, pulse.WithHeader(k, v))
			}
			c, err := cf.connect()
			if err != nil {
				return err
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			ack, err := c.Publish(ctx, args[0], typ, data, opts...)
			if err != nil {
				return err
			}
			dup := ""
			if ack.Duplicate {
				dup = " (duplicate)"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "published %s seq=%d id=%s%s\n", args[0], ack.Sequence, ack.ID, dup)
			return err
		},
	}
	cf.register(cmd)
	cmd.Flags().StringVarP(&typ, "type", "t", "", "ce-type (empty publishes an untyped event)")
	cmd.Flags().StringVar(&id, "id", "", "event ID / Nats-Msg-Id (default: new ULID)")
	cmd.Flags().StringVar(&contentType, "content-type", "", "content type (default: detected)")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "extra header key=value (repeatable)")
	return cmd
}

// parseSince accepts RFC 3339 timestamps, dates (2026-01-01), and relative durations (90m, 24h, 7d, 2w).
func parseSince(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	if len(s) > 1 {
		unit := s[len(s)-1]
		if n, err := strconv.Atoi(s[:len(s)-1]); err == nil && n >= 0 {
			switch unit {
			case 'd':
				return now.Add(-time.Duration(n) * 24 * time.Hour), nil
			case 'w':
				return now.Add(-time.Duration(n) * 7 * 24 * time.Hour), nil
			}
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse --since %q (use RFC 3339, YYYY-MM-DD, or a duration like 24h, 7d)", s)
}

type jsonEvent struct {
	Stream      string              `json:"stream"`
	Seq         uint64              `json:"seq"`
	StreamTime  time.Time           `json:"stream_time"`
	Subject     string              `json:"subject"`
	ID          string              `json:"id,omitempty"`
	Type        string              `json:"type,omitempty"`
	Source      string              `json:"source,omitempty"`
	Time        *time.Time          `json:"time,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Data        json.RawMessage     `json:"data,omitempty"`
	DataText    string              `json:"data_text,omitempty"`
	DataB64     string              `json:"data_b64,omitempty"`
}

func toJSON(e pulse.Event) jsonEvent {
	j := jsonEvent{Stream: e.Stream, Seq: e.Sequence, StreamTime: e.StreamTime, Subject: e.Subject, ID: e.ID, Type: e.Type,
		Source: e.Source, ContentType: e.ContentType, Headers: e.Extra}
	if !e.Time.IsZero() {
		t := e.Time
		j.Time = &t
	}
	switch {
	case len(e.Data) == 0:
	case json.Valid(e.Data):
		j.Data = e.Data
	case utf8.Valid(e.Data):
		j.DataText = string(e.Data)
	default:
		j.DataB64 = base64.StdEncoding.EncodeToString(e.Data)
	}
	return j
}

func oneLine(e pulse.Event) string {
	typ := e.Type
	if typ == "" {
		typ = "(untyped)"
	}
	data := string(e.Data)
	if !utf8.Valid(e.Data) {
		data = fmt.Sprintf("<%d bytes binary>", len(e.Data))
	}
	data = strings.ReplaceAll(data, "\n", " ")
	if len(data) > 200 {
		data = data[:200] + "…"
	}
	return fmt.Sprintf("%d %s %s %s %s", e.Sequence, e.StreamTime.UTC().Format(time.RFC3339Nano), e.Subject, typ, data)
}

func newSubCmd() *cobra.Command {
	var cf connFlags
	var since, durable string
	var seq uint64
	var onlyNew, asJSON bool
	var limit int
	cmd := &cobra.Command{
		Use:   "sub <filter>",
		Short: "Subscribe to events; replays history with --since or --seq",
		Example: `  pulse sub 'evt.home.>' --since 2026-01-01
  pulse sub 'evt.github.>' --since 24h --json
  pulse sub 'evt.home.>' --durable indexer`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts []pulse.SubOption
			switch {
			case since != "":
				t, err := parseSince(since, time.Now())
				if err != nil {
					return err
				}
				opts = append(opts, pulse.Since(t))
			case seq > 0:
				opts = append(opts, pulse.FromSeq(seq))
			case onlyNew:
				opts = append(opts, pulse.OnlyNew())
			}
			if durable != "" {
				opts = append(opts, pulse.Durable(durable))
			}
			c, err := cf.connect()
			if err != nil {
				return err
			}
			defer c.Close()
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			out := cmd.OutOrStdout()
			enc := json.NewEncoder(out)
			n := 0
			done := make(chan struct{})
			events := make(chan pulse.Event, 256)
			handler := func(_ context.Context, e pulse.Event) error {
				select {
				case events <- e:
				case <-ctx.Done():
				}
				return nil
			}
			opts = append(opts, pulse.OnError(func(_ pulse.Event, err error) {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
			}))
			s, err := c.Subscribe(ctx, args[0], handler, opts...)
			if err != nil {
				return err
			}
			defer s.Stop()
			go func() {
				defer close(done)
				for {
					select {
					case <-ctx.Done():
						return
					case e := <-events:
						if asJSON {
							_ = enc.Encode(toJSON(e))
						} else {
							_, _ = fmt.Fprintln(out, oneLine(e))
						}
						n++
						if limit > 0 && n >= limit {
							cancel()
							return
						}
					}
				}
			}()
			<-done
			return nil
		},
	}
	cf.register(cmd)
	cmd.Flags().StringVar(&since, "since", "", "replay from a time: RFC 3339, YYYY-MM-DD, or a duration (24h, 7d)")
	cmd.Flags().Uint64Var(&seq, "seq", 0, "replay from a stream sequence")
	cmd.Flags().BoolVar(&onlyNew, "new", false, "only events published from now on")
	cmd.Flags().StringVar(&durable, "durable", "", "durable consumer name (needs a durable grant)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object per event")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "exit after N events")
	return cmd
}
