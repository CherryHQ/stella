package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:static
var staticFS embed.FS

// StaticHandler serves files from the embedded static/ directory at GET /static/.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: embed sub failed: " + err.Error())
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// spaShell is served for all SPA routes when the built index.html is absent.
const spaShell = `<!doctype html><html><head><meta charset="utf-8"><title>stella</title></head><body><div id="app-root"></div></body></html>`

// SPAHandler serves the built React SPA. For paths matching a file in static/dist/
// it serves that file; all other paths receive index.html (SPA fallback).
// When the SPA has not been built, a minimal HTML shell is served so that
// the server stays functional and tests can assert on page routes.
func SPAHandler() http.Handler {
	indexHTML, _ := staticFS.ReadFile("static/dist/index.html")
	if indexHTML == nil {
		indexHTML = []byte(spaShell)
	}
	dist, err := fs.Sub(staticFS, "static/dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexHTML)
		})
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if f, err := dist.Open(p); err == nil {
			_ = f.Close()
			w.Header().Set("Cache-Control", cacheControlForSPAPath(p))
			fileServer.ServeHTTP(w, r)
			return
		}
		// A missing hashed asset is a file that no longer exists, not a SPA
		// route. Answering it with the shell and a 200 hands a stale client
		// HTML under a script URL, which any cache-first store then keeps
		// forever — and because the hashes are deterministic, a rollback that
		// restores the real file still gets HTML from that cache.
		if strings.HasPrefix(p, "assets/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
}

func cacheControlForSPAPath(path string) string {
	if strings.HasPrefix(path, "assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}
