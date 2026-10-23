// Package natsconn opens pulsed's internal NATS connections (SYS and APP users) over TLS (ADR-0023).
package natsconn

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Options for an internal connection.
type Options struct {
	URL         string
	TLS         *tls.Config
	JWT         string
	Seed        []byte
	Name        string
	InboxPrefix string
	Logger      *slog.Logger
}

// Connect dials NATS and keeps reconnecting forever. It fails only if the first connect does not succeed before ctx ends.
func Connect(ctx context.Context, o Options) (*nats.Conn, error) {
	if o.TLS == nil {
		return nil, errors.New("natsconn: TLS config is required")
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("conn", o.Name)
	opts := []nats.Option{
		nats.Name(o.Name),
		nats.Secure(o.TLS),
		nats.UserJWTAndSeed(o.JWT, string(o.Seed)),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.ReconnectJitter(500*time.Millisecond, 2*time.Second),
		nats.Timeout(5 * time.Second),
		nats.PingInterval(20 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("nats disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) { log.Info("nats reconnected", "url", nc.ConnectedUrlRedacted()) }),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subj := ""
			if sub != nil {
				subj = sub.Subject
			}
			log.Error("nats async error", "err", err, "subject", subj)
		}),
	}
	if o.InboxPrefix != "" {
		opts = append(opts, nats.CustomInboxPrefix(o.InboxPrefix))
	}
	var lastErr error
	for {
		nc, err := nats.Connect(o.URL, opts...)
		if err == nil {
			log.Info("nats connected", "url", nc.ConnectedUrlRedacted(), "server", nc.ConnectedServerName())
			return nc, nil
		}
		lastErr = err
		log.Warn("nats connect failed; retrying", "err", err)
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lastErr)
		case <-time.After(time.Second):
		}
	}
}
