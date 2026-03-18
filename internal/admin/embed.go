package admin

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/static
var staticFS embed.FS

// staticHandler returns an http.Handler that serves files from the
// embedded ui/static directory, stripping the "/static/" prefix so
// that a request for GET /static/js/api.js resolves to ui/static/js/api.js.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "ui/static")
	if err != nil {
		// Should never happen — the embedded path is compile-time valid.
		panic("admin: embed sub failed: " + err.Error())
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
