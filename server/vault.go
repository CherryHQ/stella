package server

import (
	"net/http"
)

// vaultEntryResponse is the JSON shape returned by listVaultEntries.
type vaultEntryResponse struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// listVaultEntries handles GET /api/auth/profile/vault.
func (s *Server) listVaultEntries(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	entries, err := s.vaultSvc.List(r.Context(), info.UserID)
	if err != nil {
		s.log.Error("list vault entries", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := make([]vaultEntryResponse, len(entries))
	for i, e := range entries {
		resp[i] = vaultEntryResponse{
			Name:      e.Name,
			CreatedAt: e.CreatedAt,
			UpdatedAt: e.UpdatedAt,
		}
	}
	writeData(w, http.StatusOK, resp)
}

// setVaultEntry handles PUT /api/auth/profile/vault/{name}.
func (s *Server) setVaultEntry(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	if err := s.vaultSvc.Set(r.Context(), info.UserID, name, body.Value); err != nil {
		s.log.Error("set vault entry", "user_id", info.UserID, "name", name, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// deleteVaultEntry handles DELETE /api/auth/profile/vault/{name}.
func (s *Server) deleteVaultEntry(w http.ResponseWriter, r *http.Request) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if err := s.vaultSvc.Delete(r.Context(), info.UserID, name); err != nil {
		s.log.Error("delete vault entry", "user_id", info.UserID, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
