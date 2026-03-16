package admin

import (
	"embed"
	"net/http"
)

//go:embed ui/index.html
var adminUI embed.FS

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	data, err := adminUI.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "admin UI not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
