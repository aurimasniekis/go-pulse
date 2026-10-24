// Package notify sends alert notifications to Telegram and Discord (PL-OBS-006). Tokens and webhook URLs come from
// files and never appear in logs or errors: HTTP errors are stripped of their URL before they leave this package.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/metrics"
)

// Severity levels, lowest first.
const (
	Info     = "info"
	Warning  = "warning"
	Critical = "critical"
)

// Rank orders severities (unknown = 0).
func Rank(s string) int {
	switch s {
	case Info:
		return 1
	case Warning:
		return 2
	case Critical:
		return 3
	}
	return 0
}

// Message is one notification.
type Message struct {
	Title    string    // rule name
	Text     string    // summary
	Severity string    // info | warning | critical
	State    string    // firing | resolved | reminder | test
	Key      string    // alert instance key (stream, certificate, client …), may be empty
	At       time.Time // when the state changed
}

// Sender delivers a message to one service.
type Sender interface {
	Kind() string
	Send(ctx context.Context, m Message) error
}

// NotifierStatus is a notifier's counters for the Alerts page.
type NotifierStatus struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind" enum:"telegram,discord"`
	MinSeverity string    `json:"min_severity"`
	Sent        int64     `json:"sent"`
	Failed      int64     `json:"failed"`
	Suppressed  int64     `json:"suppressed" doc:"Dropped by the rate limit or a full queue"`
	LastSent    time.Time `json:"last_sent,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
}

// Notifier wraps a Sender with a severity floor, a token-bucket rate limit and an async queue.
type Notifier struct {
	name   string
	sender Sender
	min    string
	log    *slog.Logger

	rate   float64 // tokens per second
	burst  float64
	queue  chan Message
	mu     sync.Mutex
	tokens float64
	last   time.Time
	st     NotifierStatus
	held   int // suppressed since the last successful send (reported in the next message)
	now    func() time.Time
}

// Options for a Notifier.
type Options struct {
	MinSeverity string  // default warning
	PerMinute   float64 // default 20
	Burst       int     // default 10
	Log         *slog.Logger
}

// New wraps sender.
func New(name string, sender Sender, o Options) *Notifier {
	if o.MinSeverity == "" {
		o.MinSeverity = Warning
	}
	if o.PerMinute <= 0 {
		o.PerMinute = 20
	}
	if o.Burst <= 0 {
		o.Burst = 10
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	n := &Notifier{name: name, sender: sender, min: o.MinSeverity, log: o.Log.With("notifier", name), rate: o.PerMinute / 60,
		burst: float64(o.Burst), tokens: float64(o.Burst), queue: make(chan Message, 100), now: time.Now}
	n.st = NotifierStatus{Name: name, Kind: sender.Kind(), MinSeverity: o.MinSeverity}
	return n
}

// Name of the notifier.
func (n *Notifier) Name() string { return n.name }

// Status returns counters.
func (n *Notifier) Status() NotifierStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.st
}

// Wants reports whether the notifier takes messages of this severity.
func (n *Notifier) Wants(severity string) bool { return Rank(severity) >= Rank(n.min) }

func (n *Notifier) take() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if !n.last.IsZero() {
		n.tokens = min(n.burst, n.tokens+now.Sub(n.last).Seconds()*n.rate)
	}
	n.last = now
	if n.tokens < 1 {
		return false
	}
	n.tokens--
	return true
}

func (n *Notifier) suppress(why string) {
	n.mu.Lock()
	n.st.Suppressed++
	n.held++
	n.mu.Unlock()
	metrics.Notifications.WithLabelValues(n.name, "suppressed").Inc()
	n.log.Warn("notification suppressed", "why", why)
}

// Enqueue queues m if its severity passes the floor (resolved messages follow the severity of the alert).
func (n *Notifier) Enqueue(m Message) {
	if !n.Wants(m.Severity) {
		return
	}
	select {
	case n.queue <- m:
	default:
		n.suppress("queue full")
	}
}

// Run delivers queued messages until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			if !n.take() {
				n.suppress("rate limit")
				continue
			}
			_ = n.deliver(ctx, m)
		}
	}
}

// Test sends a test message now (subject to the rate limit, not the severity floor).
func (n *Notifier) Test(ctx context.Context) error {
	if !n.take() {
		n.suppress("rate limit")
		return errors.New("rate limited; try again in a minute")
	}
	return n.deliver(ctx, Message{Title: "Test notification", Text: "Pulse can reach this " + n.sender.Kind() + " notifier.",
		Severity: Info, State: "test", At: n.now()})
}

func (n *Notifier) deliver(ctx context.Context, m Message) error {
	n.mu.Lock()
	if n.held > 0 {
		m.Text += fmt.Sprintf("\n(%d earlier notification(s) were suppressed by the rate limit)", n.held)
	}
	n.mu.Unlock()
	var err error
	for attempt, wait := range []time.Duration{0, time.Second, 5 * time.Second} {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = n.sender.Send(sctx, m)
		cancel()
		var re *retryable
		if err == nil || !errors.As(err, &re) || attempt == 2 {
			break
		}
		if re.after > 0 && re.after <= 30*time.Second {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(re.after):
			}
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err != nil {
		n.st.Failed++
		n.st.LastError = err.Error()
		metrics.Notifications.WithLabelValues(n.name, "failed").Inc()
		n.log.Warn("notification failed", "err", err)
		return err
	}
	n.st.Sent++
	n.st.LastSent, n.st.LastError, n.held = n.now().UTC(), "", 0
	metrics.Notifications.WithLabelValues(n.name, "sent").Inc()
	return nil
}

// retryable marks 429/5xx/network errors.
type retryable struct {
	err   error
	after time.Duration
}

func (r *retryable) Error() string { return r.err.Error() }
func (r *retryable) Unwrap() error { return r.err }

// post sends JSON and maps the response. The URL contains the secret, so transport errors are re-wrapped without it.
func post(ctx context.Context, hc *http.Client, service, u string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%s: invalid endpoint", service)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pulsed")
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &retryable{err: fmt.Errorf("%s: %w", service, err)}
	}
	defer resp.Body.Close()
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return &retryable{err: fmt.Errorf("%s: HTTP %d", service, resp.StatusCode), after: time.Duration(after) * time.Second}
	default:
		return fmt.Errorf("%s: HTTP %d: %s", service, resp.StatusCode, strings.TrimSpace(truncate(string(excerpt))))
	}
}

// truncate keeps service error bodies short (Telegram and Discord do not echo tokens in error bodies).
func truncate(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func icon(m Message) string {
	switch {
	case m.State == "resolved":
		return "✅"
	case m.State == "test":
		return "🔔"
	case m.Severity == Critical:
		return "🔴"
	case m.Severity == Warning:
		return "🟠"
	default:
		return "🔵"
	}
}

func headline(m Message) string {
	state := map[string]string{"firing": "FIRING", "resolved": "RESOLVED", "reminder": "STILL FIRING", "test": "TEST"}[m.State]
	if state == "" {
		state = strings.ToUpper(m.State)
	}
	return fmt.Sprintf("%s %s [%s] %s", icon(m), state, strings.ToUpper(m.Severity), m.Title)
}

// Telegram sends with the Bot API (sendMessage, HTML parse mode).
type Telegram struct {
	APIURL string // default https://api.telegram.org
	Token  string
	ChatID string
	HTTP   *http.Client
}

// Kind implements Sender.
func (t *Telegram) Kind() string { return "telegram" }

// Send implements Sender.
func (t *Telegram) Send(ctx context.Context, m Message) error {
	base := t.APIURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	text := "<b>" + html.EscapeString(headline(m)) + "</b>\n" + html.EscapeString(m.Text)
	if m.Key != "" {
		text += "\n<code>" + html.EscapeString(m.Key) + "</code>"
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	return post(ctx, t.HTTP, "telegram", strings.TrimRight(base, "/")+"/bot"+t.Token+"/sendMessage", map[string]any{
		"chat_id": t.ChatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true,
	})
}

// Discord sends to an incoming webhook with one embed and no mentions.
type Discord struct {
	WebhookURL string
	HTTP       *http.Client
}

// Kind implements Sender.
func (d *Discord) Kind() string { return "discord" }

var discordColor = map[string]int{Critical: 0xE03131, Warning: 0xF08C00, Info: 0x4263EB}

// Send implements Sender.
func (d *Discord) Send(ctx context.Context, m Message) error {
	color := discordColor[m.Severity]
	if m.State == "resolved" {
		color = 0x2F9E44
	}
	desc := m.Text
	if m.Key != "" {
		desc += "\n`" + strings.ReplaceAll(m.Key, "`", "'") + "`"
	}
	if len(desc) > 3500 {
		desc = desc[:3500]
	}
	title := headline(m)
	if len(title) > 250 {
		title = title[:250]
	}
	return post(ctx, d.HTTP, "discord", d.WebhookURL, map[string]any{
		"username":         "Pulse",
		"allowed_mentions": map[string]any{"parse": []string{}},
		"embeds":           []map[string]any{{"title": title, "description": desc, "color": color, "timestamp": m.At.UTC().Format(time.RFC3339)}},
	})
}
