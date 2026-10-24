package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUIHandler(t *testing.T) {
	get := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	// PL-UI-010: no built UI → a plain notice, never a 500; API paths still 404.
	missing := uiHandlerFS(nil)
	if r := get(missing, "/explorer"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "was not built") || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing UI: %d %v %s", r.Code, r.Header(), r.Body)
	}
	if r := get(missing, "/api/nope"); r.Code != http.StatusNotFound {
		t.Fatalf("api path: %d", r.Code)
	}
	built := uiHandlerFS(fstest.MapFS{
		"index.html":      {Data: []byte("<html>app</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	})
	if r := get(built, "/alerts"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "app") || r.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("SPA fallback: %d %v", r.Code, r.Header())
	}
	if r := get(built, "/assets/app-1.js"); r.Code != http.StatusOK || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", r.Code, r.Header())
	}
	if r := get(built, "/api/admin/v1/unknown"); r.Code != http.StatusNotFound {
		t.Fatalf("api path through UI: %d", r.Code)
	}
}
