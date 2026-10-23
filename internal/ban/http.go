package ban

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Middleware rejects requests from banned addresses with 403 (PL-BAN-006).
func (s *Service) Middleware(clientIP func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.IsBanned(clientIP(r)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BansResponse is what banagent reads.
type BansResponse struct {
	Generation uint64    `json:"generation"`
	Generated  time.Time `json:"generated"`
	Bans       []Entry   `json:"bans"`
}

// Feed serves GET /internal/v1/bans for banagent (bearer token from banagent.token). With ?wait=<generation> it
// long-polls up to 25 s until the ban set changes, so new bans reach nftables within a second.
type Feed struct {
	s     *Service
	token []byte

	mu   sync.Mutex
	gen  uint64
	wake chan struct{}
}

// NewFeed wires a feed to the service.
func NewFeed(s *Service, token string) *Feed {
	f := &Feed{s: s, token: []byte(strings.TrimSpace(token)), gen: 1, wake: make(chan struct{})}
	s.OnChange(f.bump)
	return f
}

func (f *Feed) bump() {
	f.mu.Lock()
	f.gen++
	close(f.wake)
	f.wake = make(chan struct{})
	f.mu.Unlock()
}

func (f *Feed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	got := []byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if len(f.token) == 0 || subtle.ConstantTimeCompare(got, f.token) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	gen, wake := f.gen, f.wake
	f.mu.Unlock()
	if want := r.URL.Query().Get("wait"); want != "" && want == uitoa(gen) {
		select {
		case <-wake:
		case <-time.After(25 * time.Second):
		case <-r.Context().Done():
			return
		}
		f.mu.Lock()
		gen = f.gen
		f.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(BansResponse{Generation: gen, Generated: time.Now().UTC(), Bans: f.s.Snapshot()})
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
