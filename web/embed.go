package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var staticFS embed.FS

// StaticHandler serves files from the embedded static/ directory at GET /static/.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: embed sub failed: " + err.Error())
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// SPAHandler serves the built React SPA. For paths matching a file in static/dist/
// it serves that file; all other paths receive index.html (SPA fallback).
func SPAHandler() http.Handler {
	dist, err := fs.Sub(staticFS, "static/dist")
	if err != nil {
		panic("web: dist sub failed: " + err.Error())
	}
	indexHTML, _ := staticFS.ReadFile("static/dist/index.html")
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if f, err := dist.Open(p); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		if indexHTML == nil {
			http.Error(w, "spa not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
}
