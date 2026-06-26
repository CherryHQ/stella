package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/mlruntime"
)

// GetOCRSettings returns the deployment-wide local-OCR configuration, plus whether
// the OCR models are actually installed so the UI can warn when enabling would be
// a no-op.
func (s *Server) GetOCRSettings(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	cfg, err := config.LoadOCRSettings(r.Context(), s.store)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, ocrSettingsView(cfg))
}

// UpdateOCRSettings persists the local-OCR configuration. The change takes effect
// at runtime: the document extractor checks this setting on its next OCR-eligible
// read, so no restart is needed.
func (s *Server) UpdateOCRSettings(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	next := config.OCRSettings{Enabled: body.Enabled}
	if err := config.SaveOCRSettings(r.Context(), s.store, next); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, ocrSettingsView(next))
}

func ocrSettingsView(c config.OCRSettings) map[string]any {
	return map[string]any{
		"enabled":   c.Enabled,
		"available": ocrModelsInstalled(),
	}
}

// ocrModelsInstalled reports whether the sidecar OCR models resolved on this host.
// A best-effort probe: a resolve error or a missing runtime both read as "not
// installed", which is all the UI needs to decide whether to warn.
func ocrModelsInstalled() bool {
	r, found, err := mlruntime.Resolve(config.StellaHome())
	return err == nil && found && r.HasOCR()
}
