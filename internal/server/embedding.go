package server

import (
	"fmt"
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/embedding"
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

	// Bound the dimension before persisting: a value past the vector(StorageDim)
	// storage width (or negative) would be accepted here only to be rejected later
	// by the embedding provider, silently disabling the lane. 0 is allowed and
	// means "use the model's native width" (LoadEmbeddingSettings backfills it).
	if body.Dim < 0 || body.Dim > embedding.StorageDim {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("dim must be between 0 and %d", embedding.StorageDim))
		return
	}

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

func embeddingSettingsView(c config.EmbeddingSettings) map[string]any {
	return map[string]any{
		"enabled":   c.Enabled,
		"dim":       c.Dim,
		"normalize": c.Normalize,
	}
}
