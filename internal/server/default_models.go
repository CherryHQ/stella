package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/platform/config"
)

// maxModelRefLen caps a stored model reference so a stray paste cannot bloat
// the setting row.
const maxModelRefLen = 256

// GetDefaultModels returns the deployment-wide default model configuration.
func (s *Server) GetDefaultModels(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	cfg, err := access.GetDefaultModels(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, defaultModelsView(cfg))
}

// UpdateDefaultModels persists the deployment default models and rebuilds
// runner factories. Future runners use the new settings; already admitted
// runners finish against their captured configuration. The embedding lane
// re-reads its model on the next pass, so it needs no rebuild.
func (s *Server) UpdateDefaultModels(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var body struct {
		Model               string `json:"model"`
		ModelThinking       string `json:"model_thinking"`
		ModelStrong         string `json:"model_strong"`
		ModelStrongThinking string `json:"model_strong_thinking"`
		ModelFast           string `json:"model_fast"`
		ModelFastThinking   string `json:"model_fast_thinking"`
		ModelVision         string `json:"model_vision"`
		ModelEmbedding      string `json:"model_embedding"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	for _, ref := range []string{
		body.Model, body.ModelStrong, body.ModelFast, body.ModelVision, body.ModelEmbedding,
	} {
		if len(ref) > maxModelRefLen {
			writeError(w, http.StatusBadRequest, "model exceeds maximum length")
			return
		}
	}

	next, err := access.SetDefaultModels(r.Context(), config.DefaultModels{
		Model:               body.Model,
		ModelThinking:       body.ModelThinking,
		ModelStrong:         body.ModelStrong,
		ModelStrongThinking: body.ModelStrongThinking,
		ModelFast:           body.ModelFast,
		ModelFastThinking:   body.ModelFastThinking,
		ModelVision:         body.ModelVision,
		ModelEmbedding:      body.ModelEmbedding,
	})
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, defaultModelsView(next))
}

func defaultModelsView(c config.DefaultModels) map[string]any {
	return map[string]any{
		"model":                 c.Model,
		"model_thinking":        c.ModelThinking,
		"model_strong":          c.ModelStrong,
		"model_strong_thinking": c.ModelStrongThinking,
		"model_fast":            c.ModelFast,
		"model_fast_thinking":   c.ModelFastThinking,
		"model_vision":          c.ModelVision,
		"model_embedding":       c.ModelEmbedding,
	}
}
