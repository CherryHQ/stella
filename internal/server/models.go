package server

import (
	"net/http"
)

// ListModels returns the models available for selection: enabled models from
// enabled providers plus the fetched cache, computed by the control-plane
// catalog read model. No provider API calls — reads only from the DB.
func (s *Server) ListModels(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	models, err := s.controlPlane.ListEnabledModels(r.Context(), authority)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"models": models})
}
