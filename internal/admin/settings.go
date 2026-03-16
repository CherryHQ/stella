package admin

import (
	"encoding/json"
	"net/http"
)

func (s *Server) getSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	val, err := s.store.GetSetting(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Try to parse as JSON for structured output; fall back to raw string.
	var parsed any
	if err := json.Unmarshal([]byte(val), &parsed); err == nil {
		writeData(w, http.StatusOK, map[string]any{"key": key, "value": parsed})
		return
	}
	writeData(w, http.StatusOK, map[string]any{"key": key, "value": val})
}

func (s *Server) updateSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body struct {
		Value any `json:"value"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Marshal value to JSON string for storage.
	var val string
	switch v := body.Value.(type) {
	case string:
		val = v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to serialize value")
			return
		}
		val = string(data)
	}

	if err := s.store.SetSetting(r.Context(), key, val); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"key": key, "status": "updated"})
}
