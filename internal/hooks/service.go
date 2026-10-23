package hooks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/oklog/ulid/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// Errors.
var (
	ErrNotFound = errors.New("hook not found")
	ErrInvalid  = errors.New("invalid hook")
)

// Hook is an inbound webhook endpoint (never includes the secret).
type Hook struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind" enum:"github,stripe,hmac,secret"`
	Subject        string    `json:"subject"`
	Config         Config    `json:"config"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastReceivedAt time.Time `json:"last_received_at,omitzero"`
	Received       int64     `json:"received"`
	Rejected       int64     `json:"rejected"`
}

// Service stores hooks and serves /hooks/{name}.
type Service struct {
	db     *store.DB
	js     jetstream.JetStream
	master []byte
	log    *slog.Logger
	now    func() time.Time
	// OnFailure is told about bad signatures (the ban detector, M8). May be nil.
	OnFailure func(ip, source string)
	// ClientIP extracts the real client IP (Cloudflare-aware in M8). Defaults to the TCP peer.
	ClientIP func(*http.Request) string
}

// New returns a hook service.
func New(db *store.DB, js jetstream.JetStream, master []byte, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: db, js: js, master: master, log: log, now: store.Now, ClientIP: peerIP}
}

func peerIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// Input creates or updates a hook.
type Input struct {
	Name    string
	Kind    string
	Subject string // default evt.hook.<name>.received
	Config  Config
	Secret  string // empty generates one on create, keeps the current one on update
	Enabled bool
}

// GenerateSecret returns 32 random bytes base64url (no padding).
func GenerateSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (in *Input) validate() error {
	if !nameRe.MatchString(in.Name) {
		return fmt.Errorf("%w: name must match %s", ErrInvalid, nameRe)
	}
	switch in.Kind {
	case KindGitHub, KindStripe, KindHMAC, KindSecret:
	default:
		return fmt.Errorf("%w: kind must be github, stripe, hmac or secret", ErrInvalid)
	}
	if in.Subject == "" {
		in.Subject = "evt.hook." + strings.ReplaceAll(in.Name, "_", "-") + ".received"
	}
	if err := envelope.ValidateSubject(in.Subject); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if in.Config.Encoding != "" && in.Config.Encoding != "hex" && in.Config.Encoding != "base64" {
		return fmt.Errorf("%w: encoding must be hex or base64", ErrInvalid)
	}
	if in.Secret != "" && len(in.Secret) < 16 {
		return fmt.Errorf("%w: secrets must be at least 16 characters", ErrInvalid)
	}
	return nil
}

// Create stores a hook; it returns the secret (generated when not given), shown once.
func (s *Service) Create(ctx context.Context, in Input, actor audit.Actor) (*Hook, string, error) {
	if err := in.validate(); err != nil {
		return nil, "", err
	}
	if in.Secret == "" {
		in.Secret = GenerateSecret()
	}
	enc, err := secretbox.Seal(s.master, []byte(in.Secret))
	if err != nil {
		return nil, "", err
	}
	cfg, _ := json.Marshal(in.Config)
	now := s.now()
	id := strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String())
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hooks_in(id, name, kind, subject, secret_enc, config, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Name, in.Kind, in.Subject, enc, string(cfg), boolInt(in.Enabled), store.Ms(now), store.Ms(now)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: a hook named %q exists", ErrInvalid, in.Name)
			}
			return err
		}
		return audit.Insert(ctx, tx, actor, "hook_in.create", id, audit.OK, map[string]any{"name": in.Name, "kind": in.Kind, "subject": in.Subject})
	})
	if err != nil {
		return nil, "", err
	}
	h, err := s.Get(ctx, id)
	return h, in.Secret, err
}

// Update changes a hook; a non-empty Secret replaces the stored one.
func (s *Service) Update(ctx context.Context, id string, in Input, actor audit.Actor) (*Hook, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	cfg, _ := json.Marshal(in.Config)
	var enc []byte
	if in.Secret != "" {
		var err error
		if enc, err = secretbox.Seal(s.master, []byte(in.Secret)); err != nil {
			return nil, err
		}
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE hooks_in SET name = ?, kind = ?, subject = ?, config = ?, enabled = ?, updated_at = ?,
			secret_enc = COALESCE(?, secret_enc) WHERE id = ?`, in.Name, in.Kind, in.Subject, string(cfg), boolInt(in.Enabled), store.Ms(s.now()), enc, id)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: a hook named %q exists", ErrInvalid, in.Name)
			}
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit.Insert(ctx, tx, actor, "hook_in.update", id, audit.OK, map[string]any{"name": in.Name, "secret_rotated": in.Secret != ""})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Delete removes a hook.
func (s *Service) Delete(ctx context.Context, id string, actor audit.Actor) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM hooks_in WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit.Insert(ctx, tx, actor, "hook_in.delete", id, audit.OK, nil)
	})
}

const cols = `id, name, kind, subject, config, enabled, created_at, updated_at, last_received_at, received, rejected`

func scan(row interface{ Scan(...any) error }) (*Hook, error) {
	var h Hook
	var cfg string
	var en int
	var c, u, l int64
	if err := row.Scan(&h.ID, &h.Name, &h.Kind, &h.Subject, &cfg, &en, &c, &u, &l, &h.Received, &h.Rejected); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(cfg), &h.Config)
	h.Enabled = en == 1
	h.CreatedAt, h.UpdatedAt, h.LastReceivedAt = store.FromMs(c), store.FromMs(u), store.FromMs(l)
	return &h, nil
}

// Get returns one hook.
func (s *Service) Get(ctx context.Context, id string) (*Hook, error) {
	h, err := scan(s.db.R().QueryRowContext(ctx, "SELECT "+cols+" FROM hooks_in WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// List returns all hooks.
func (s *Service) List(ctx context.Context) ([]*Hook, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT "+cols+" FROM hooks_in ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Hook{}
	for rows.Next() {
		h, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// event derives the envelope fields for a verified delivery.
func event(h *Hook, r *http.Request, body []byte) envelope.Event {
	e := envelope.Event{Subject: h.Subject, Source: "hook:" + h.Name, ContentType: r.Header.Get("Content-Type"), Data: body,
		Type: h.Config.Type, Extra: map[string][]string{}}
	switch h.Kind {
	case KindGitHub:
		ev := r.Header.Get("X-GitHub-Event")
		if e.Type == "" && ev != "" {
			e.Type = "github." + ev
		}
		e.ID = r.Header.Get("X-GitHub-Delivery")
		for _, k := range []string{"X-GitHub-Event", "X-GitHub-Delivery", "X-GitHub-Hook-ID"} {
			if v := r.Header.Get(k); v != "" {
				e.Extra[k] = []string{v}
			}
		}
	case KindStripe:
		var p struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(body, &p) == nil {
			if e.Type == "" && p.Type != "" {
				e.Type = "stripe." + p.Type
			}
			e.ID = p.ID
		}
	default:
		if v := r.Header.Get("Idempotency-Key"); v != "" && len(v) <= 128 {
			e.ID = v
		}
	}
	if e.Type == "" {
		e.Type = "hook." + h.Name
	}
	return e
}

// Handler serves POST /hooks/{name} (PL-HOOK-001/004).
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := r.PathValue("name")
		h, secret, err := s.lookup(r.Context(), name)
		if err != nil || !h.Enabled {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ip := s.ClientIP(r)
		if err := Verify(h.Kind, h.Config, secret, r.Header, body, s.now()); err != nil {
			s.count(r.Context(), h.ID, false)
			if s.OnFailure != nil {
				s.OnFailure(ip, "webhook")
			}
			s.log.Info("webhook signature rejected", "hook", h.Name, "ip", ip)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		msg, err := envelope.Encode(event(h, r, body))
		if err != nil {
			http.Error(w, "hook subject is invalid", http.StatusInternalServerError)
			return
		}
		ack, err := s.js.PublishMsg(r.Context(), msg)
		if err != nil {
			s.log.Error("webhook publish", "hook", h.Name, "err", err)
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		s.count(r.Context(), h.ID, true)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"seq": ack.Sequence, "duplicate": ack.Duplicate})
	})
}

func (s *Service) lookup(ctx context.Context, name string) (*Hook, []byte, error) {
	var id string
	var enc []byte
	if err := s.db.R().QueryRowContext(ctx, "SELECT id, secret_enc FROM hooks_in WHERE name = ?", name).Scan(&id, &enc); err != nil {
		return nil, nil, ErrNotFound
	}
	secret, err := secretbox.Open(s.master, enc)
	if err != nil {
		return nil, nil, err
	}
	h, err := s.Get(ctx, id)
	return h, secret, err
}

func (s *Service) count(ctx context.Context, id string, ok bool) {
	ctx = context.WithoutCancel(ctx) // a client hanging up must not lose the counter update
	_ = s.db.Write(ctx, func(tx *sql.Tx) error {
		if ok {
			_, err := tx.ExecContext(ctx, "UPDATE hooks_in SET received = received + 1, last_received_at = ? WHERE id = ?", store.Ms(s.now()), id)
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE hooks_in SET rejected = rejected + 1 WHERE id = ?", id)
		return err
	})
}
