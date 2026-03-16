package admin

import "net/http"

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	if s.mem == nil {
		writeData(w, http.StatusOK, []any{})
		return
	}
	sessions, err := s.mem.ListInfo(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, sessions)
}
