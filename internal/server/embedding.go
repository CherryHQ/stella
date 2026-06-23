package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
)

// GetEmbeddingSettings returns the deployment-wide embedding configuration. The
// API key is never returned; has_api_key reports whether one is stored.
func (s *Server) GetEmbeddingSettings(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	cfg, err := config.LoadEmbeddingSettings(r.Context(), s.store)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, embeddingSettingsView(cfg))
}

// UpdateEmbeddingSettings persists the embedding configuration. The change takes
// effect at runtime: the embedding service re-reads this config on its next query
// and backfill pass, so no restart is needed. The api_key is write-only — an
// omitted or empty key keeps the stored one, since the GET never echoes it back.
func (s *Server) UpdateEmbeddingSettings(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
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

	existing, err := config.LoadEmbeddingSettings(r.Context(), s.store)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	next := config.EmbeddingSettings{
		Enabled:   body.Enabled,
		Model:     body.Model,
		Dim:       body.Dim,
		BaseURL:   body.BaseURL,
		Normalize: body.Normalize,
		APIKey:    existing.APIKey, // preserve unless a new key is supplied
	}
	if body.APIKey != nil && *body.APIKey != "" {
		next.APIKey = *body.APIKey
	}

	// Enabling the lane without a key would silently no-op (the service treats a
	// keyless config as disabled); reject it so the operator gets clear feedback.
	if next.Enabled && next.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key is required to enable embedding")
		return
	}

	if err := config.SaveEmbeddingSettings(r.Context(), s.store, next); err != nil {
		s.writeInternalError(w, err)
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
