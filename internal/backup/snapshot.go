package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nats-io/nats.go"
)

// apiPrefix is the JetStream API prefix of the account pulsed's APP connection uses.
const apiPrefix = "$JS.API"

// StreamMeta is what a snapshot returns and a restore needs: the stream's config and state, kept as the server's
// JSON so no server types leak into pulsed.
type StreamMeta struct {
	Config json.RawMessage `json:"config"`
	State  json.RawMessage `json:"state"`
}

type apiError struct {
	Code        int    `json:"code"`
	ErrCode     int    `json:"err_code"`
	Description string `json:"description"`
}

func (e *apiError) Error() string { return fmt.Sprintf("jetstream: %s (%d)", e.Description, e.ErrCode) }

func apiRequest(ctx context.Context, nc *nats.Conn, subject string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m, err := nc.RequestWithContext(ctx, subject, b)
	if err != nil {
		return err
	}
	var env struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(m.Data, &env); err != nil {
		return fmt.Errorf("jetstream: bad response: %w", err)
	}
	if env.Error != nil {
		return env.Error
	}
	return json.Unmarshal(m.Data, out)
}

// Snapshot writes a stream snapshot (a tar archive inside an S2 stream, as the server produces it) to w. It includes
// consumers so durable positions survive a restore.
func Snapshot(ctx context.Context, nc *nats.Conn, stream string, w io.Writer) (*StreamMeta, error) {
	inbox := nc.NewInbox()
	ch := make(chan *nats.Msg, 64)
	sub, err := nc.ChanSubscribe(inbox, ch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	_ = sub.SetPendingLimits(-1, -1)
	if err := nc.FlushTimeout(10 * time.Second); err != nil {
		return nil, err
	}
	var meta StreamMeta
	if err := apiRequest(ctx, nc, apiPrefix+".STREAM.SNAPSHOT."+stream, map[string]any{
		"deliver_subject": inbox, "chunk_size": 1 << 20, "window_size": 8 << 20,
	}, &meta); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", stream, err)
	}
	idle := time.NewTimer(60 * time.Second)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-idle.C:
			return nil, fmt.Errorf("snapshot %s: no data for 60 s", stream)
		case m := <-ch:
			idle.Reset(60 * time.Second)
			if len(m.Data) == 0 {
				// End of stream: an empty message, with a 204 status when chunks were acknowledged, no header when the
				// whole snapshot fitted in one chunk, and a 408/500 status on failure.
				if st := m.Header.Get("Status"); st != "" && st != "204" {
					return nil, fmt.Errorf("snapshot %s: server reported %s %s", stream, st, m.Header.Get("Description"))
				}
				return &meta, nil
			}
			if _, err := w.Write(m.Data); err != nil {
				return nil, fmt.Errorf("snapshot %s: write: %w", stream, err)
			}
			if m.Reply != "" {
				if err := m.Respond(nil); err != nil {
					return nil, err
				}
			}
		}
	}
}

// ErrStreamExists means a restore target already exists (delete it first, or restore only missing streams).
var ErrStreamExists = errors.New("stream already exists")

// Restore creates stream from a snapshot read from r. The stream must not exist.
func Restore(ctx context.Context, nc *nats.Conn, stream string, meta StreamMeta, r io.Reader) error {
	var resp struct {
		DeliverSubject string `json:"deliver_subject"`
	}
	if err := apiRequest(ctx, nc, apiPrefix+".STREAM.RESTORE."+stream, meta, &resp); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && (ae.ErrCode == 10058 || ae.ErrCode == 10130) { // JSStreamNameExist(RestoreFailed)Err
			return fmt.Errorf("restore %s: %w", stream, ErrStreamExists)
		}
		return fmt.Errorf("restore %s: %w", stream, err)
	}
	buf := make([]byte, 128<<10)
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			reply, err := nc.RequestWithContext(cctx, resp.DeliverSubject, buf[:n])
			cancel()
			if err != nil {
				return fmt.Errorf("restore %s: send chunk: %w", stream, err)
			}
			if len(reply.Data) > 0 { // the server answers a chunk with an error response when the restore failed
				var e struct {
					Error *apiError `json:"error"`
				}
				if json.Unmarshal(reply.Data, &e) == nil && e.Error != nil {
					return fmt.Errorf("restore %s: %w", stream, e.Error)
				}
			}
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
		if rerr != nil {
			return fmt.Errorf("restore %s: read: %w", stream, rerr)
		}
	}
	// An empty chunk completes the transfer; the reply is the stream-create response once the server has restored it.
	rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	m, err := nc.RequestWithContext(rctx, resp.DeliverSubject, nil)
	if err != nil {
		return fmt.Errorf("restore %s: finish: %w", stream, err)
	}
	var done struct {
		Error *apiError `json:"error"`
	}
	if len(m.Data) > 0 {
		if err := json.Unmarshal(m.Data, &done); err != nil {
			return fmt.Errorf("restore %s: bad response: %w", stream, err)
		}
		if done.Error != nil {
			return fmt.Errorf("restore %s: %w", stream, done.Error)
		}
	}
	return nil
}
