package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
)

// GetVisionSettings returns the deployment-wide vision model.
func (s *Server) GetVisionSettings(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	cfg, err := access.GetVisionSettings(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, visionSettingsView(cfg))
}

// UpdateVisionSettings persists the vision model. The change takes effect on the
// next agent snapshot, so a running runner keeps its current model until it
// reloads.
func (s *Server) UpdateVisionSettings(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// Cap the length so a stray paste cannot bloat the setting row.
	if len(body.Model) > 256 {
		writeError(w, http.StatusBadRequest, "model exceeds maximum length")
		return
	}

	next, err := access.SetVisionSettings(r.Context(), config.VisionSettings{Model: body.Model})
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, visionSettingsView(next))
}

func visionSettingsView(c config.VisionSettings) map[string]any {
	return map[string]any{"model": c.Model}
}
