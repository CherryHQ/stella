package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/controlplane"
)

// GetEmbeddingSettings returns the deployment-wide embedding lane configuration.
// The model it runs on is not here: it is named on the default-models surface
// like every other model role, and its credentials come from that provider.
func (s *Server) GetEmbeddingSettings(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	cfg, err := access.GetEmbeddingSettings(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, embeddingSettingsView(cfg))
}

// UpdateEmbeddingSettings persists the embedding lane configuration. The change
// takes effect at runtime: the embedding service re-reads this config on its next
// query and backfill pass, so no restart is needed.
func (s *Server) UpdateEmbeddingSettings(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled   bool `json:"enabled"`
		Dim       int  `json:"dim"`
		Normalize bool `json:"normalize"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// The shared control-plane service validates dimensions for both HTTP and
	// conversational settings, so neither transport can persist an unusable lane.

	next, err := access.SetEmbeddingSettings(r.Context(), controlplane.EmbeddingUpdate{
		Enabled:   body.Enabled,
		Dim:       body.Dim,
		Normalize: body.Normalize,
	})
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, embeddingSettingsView(next))
}

func embeddingSettingsView(s controlplane.EmbeddingState) map[string]any {
	return map[string]any{
		"active":    s.Active,
		"dim":       s.Settings.Dim,
		"enabled":   s.Settings.Enabled,
		"normalize": s.Settings.Normalize,
	}
}
