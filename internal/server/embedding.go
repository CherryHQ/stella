package server

import (
	"fmt"
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/embedding"
)

// GetEmbeddingSettings returns the deployment-wide embedding configuration. The
// API key is never returned; has_api_key reports whether one is stored.
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

// UpdateEmbeddingSettings persists the embedding configuration. The change takes
// effect at runtime: the embedding service re-reads this config on its next query
// and backfill pass, so no restart is needed. The api_key is write-only — an
// omitted or empty key keeps the stored one, since the GET never echoes it back.
func (s *Server) UpdateEmbeddingSettings(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled   bool    `json:"enabled"`
		Model     string  `json:"model"`
		Dim       int     `json:"dim"`
		BaseURL   string  `json:"base_url"`
		Normalize bool    `json:"normalize"`
		APIKey    *string `json:"api_key"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Bound the inputs before persisting: a dim past the vector(StorageDim) storage
	// width (or negative) would be accepted here only to be rejected later by the
	// embedding provider, silently disabling the lane. 0 is allowed and means "use
	// the model's native width" (LoadEmbeddingSettings backfills it). Length caps
	// keep a stray paste from bloating the setting row.
	if body.Dim < 0 || body.Dim > embedding.StorageDim {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("dim must be between 0 and %d", embedding.StorageDim))
		return
	}
	if len(body.Model) > 256 || len(body.BaseURL) > 2048 || (body.APIKey != nil && len(*body.APIKey) > 1024) {
		writeError(w, http.StatusBadRequest, "model, base_url, or api_key exceeds maximum length")
		return
	}

	next, err := access.SetEmbeddingSettings(r.Context(), controlplane.EmbeddingUpdate{
		Enabled:   body.Enabled,
		Model:     body.Model,
		Dim:       body.Dim,
		BaseURL:   body.BaseURL,
		Normalize: body.Normalize,
		APIKey:    body.APIKey,
	})
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, embeddingSettingsView(next))
}

func embeddingSettingsView(c config.EmbeddingSettings) map[string]any {
	return map[string]any{
		"enabled":     c.Enabled,
		"model":       c.Model,
		"dim":         c.Dim,
		"base_url":    c.BaseURL,
		"normalize":   c.Normalize,
		"has_api_key": c.APIKey != "",
	}
}
