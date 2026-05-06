package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// StaticHandler returns an http.Handler that serves files from the
// embedded static directory, stripping the "/static/" prefix so
// that a request for GET /static/js/api.js resolves to static/js/api.js.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: embed sub failed: " + err.Error())
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
