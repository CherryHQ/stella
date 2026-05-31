package server

import (
	"database/sql"
	"errors"
	"net/http"
)

// vaultEntryResponse is the JSON shape returned by ListVaultEntries.
type vaultEntryResponse struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListVaultEntries handles GET /api/auth/profile/vault.
func (s *Server) ListVaultEntries(w http.ResponseWriter, r *http.Request) {
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
			CreatedAt: parseTime(e.CreatedAt),
			UpdatedAt: parseTime(e.UpdatedAt),
		}
	}
	writeData(w, http.StatusOK, resp)
}

// GetVaultEntry handles GET /api/auth/profile/vault/{name}.
func (s *Server) GetVaultEntry(w http.ResponseWriter, r *http.Request, name string) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	value, err := s.vaultSvc.Get(r.Context(), info.UserID, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "vault entry not found")
			return
		}
		s.log.Error("get vault entry", "user_id", info.UserID, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeData(w, http.StatusOK, map[string]string{"name": name, "value": value})
}

// SetVaultEntry handles PUT /api/auth/profile/vault/{name}.
func (s *Server) SetVaultEntry(w http.ResponseWriter, r *http.Request, name string) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

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

// DeleteVaultEntry handles DELETE /api/auth/profile/vault/{name}.
func (s *Server) DeleteVaultEntry(w http.ResponseWriter, r *http.Request, name string) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

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
