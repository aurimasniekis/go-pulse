package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoute(t *testing.T) {
	cases := map[string]string{
		"/api/admin/v1/streams/EVENTS": "/api/admin/v1/streams",
		"/api/admin/v1/":               "/api/admin/v1/other",
		"/api/v1/publish":              "/api/v1/publish",
		"/api/v1/Weird123":             "/api/v1/other",
		"/hooks/github-main":           "/hooks",
		"/ws":                          "/ws",
		"/explorer":                    "ui",
		"/api/nope":                    "/api/other",
		"/api/admin/v1/" + strings.Repeat("a", 40): "/api/admin/v1/other",
	}
	for in, want := range cases {
		if got := Route(in); got != want {
			t.Errorf("Route(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandlerWithoutSources(t *testing.T) {
	SetSources(Sources{})
	IngestEvents.WithLabelValues("http").Inc()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	b := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, `pulse_ingest_events_total{via="http"}`) || strings.Contains(b, "nats_up") {
		t.Fatalf("%d\n%s", rec.Code, b)
	}
}
