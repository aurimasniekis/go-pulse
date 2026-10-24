package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/aurimasniekis/go-pulse/internal/ui"
)

const missingUI = `<!doctype html><html><head><meta charset="utf-8"><title>Pulse</title></head><body style="font-family:sans-serif">
<h1>Pulse</h1><p>The dashboard was not built into this binary. Run <code>make web</code> and rebuild, or use the API under
<code>/api/admin/v1</code>.</p></body></html>`

// uiHandler serves the embedded SPA with index.html fallback (PL-UI-010 when not built).
func uiHandler() http.Handler { return uiHandlerFS(ui.FS()) }

func uiHandlerFS(fsys fs.FS) http.Handler {
	if fsys == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(missingUI))
		})
	}
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(fsys, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		// SPA route: always the current index.html.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, fsys, "index.html")
	})
}
