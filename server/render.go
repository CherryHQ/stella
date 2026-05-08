package server

import (
	"net/http"

	"github.com/vaayne/anna/web"
)

func (s *Server) pageApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := web.SPAPage().Render(r.Context(), w); err != nil {
		s.log.Error("render spa", "error", err)
	}
}
