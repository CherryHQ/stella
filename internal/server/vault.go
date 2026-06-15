package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/vault"
)

// vaultEntryResponse is the JSON shape returned by ListVaultEntries.
type vaultEntryResponse struct {
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	UserID    string    `json:"user_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListVaultEntries handles GET /api/users/me/vault.
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
		resp[i] = vaultEntryResponseFromMeta(e)
	}
	writeData(w, http.StatusOK, map[string]any{"entries": resp})
}

// GetVaultEntry handles GET /api/users/me/vault/{name}.
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

// SetVaultEntry handles PUT /api/users/me/vault/{name}.
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
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	if name == "EMAIL_CONFIG" {
		var cfg email.Config
		if err := json.Unmarshal([]byte(body.Value), &cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid EMAIL_CONFIG: malformed JSON")
			return
		}
		if err := cfg.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid email config: %v", err))
			return
		}
	}

	if err := s.vaultSvc.Set(r.Context(), info.UserID, name, body.Value); err != nil {
		s.log.Error("set vault entry", "user_id", info.UserID, "name", name, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	// Vault entries are baked into the sandbox env at session-creation time and
	// cannot be injected into a running process; closing live runners forces a
	// clean restart so the next chat turn or scheduled run reads the new value.
	// Mirrors the OAuth token write path (see credentials.Service).
	if err := s.credSvc.InvalidateUser(info.UserID); err != nil {
		s.log.Warn("invalidate user runners after vault set", "user_id", info.UserID, "name", name, "error", err)
	}

	meta, err := s.vaultSvc.GetMeta(r.Context(), info.UserID, name)
	if err != nil {
		s.log.Error("get vault entry meta", "user_id", info.UserID, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeData(w, http.StatusOK, vaultEntryResponseFromMeta(meta))
}

// DeleteVaultEntry handles DELETE /api/users/me/vault/{name}.
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

	// See SetVaultEntry: invalidate live runners so the next session sees the
	// updated vault snapshot instead of the cached one from sandbox start.
	if err := s.credSvc.InvalidateUser(info.UserID); err != nil {
		s.log.Warn("invalidate user runners after vault delete", "user_id", info.UserID, "name", name, "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListScopedVaultEntries handles GET /api/vault.
func (s *Server) ListScopedVaultEntries(w http.ResponseWriter, r *http.Request, params apiserver.ListScopedVaultEntriesParams) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	scope := vault.ScopeUser
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	userID, agentID, ok := s.resolveVaultScope(w, r, info, scope, agentID)
	if !ok {
		return
	}

	var entries []vault.EntryMeta
	var err error
	if isSystemVaultScope(scope) {
		entries, err = s.vaultSvc.ListSystemScoped(r.Context(), scope, agentID)
	} else {
		entries, err = s.vaultSvc.ListScoped(r.Context(), scope, userID, agentID)
	}
	if err != nil {
		s.log.Error("list scoped vault entries", "user_id", info.UserID, "scope", scope, "agent_id", agentID, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	resp := make([]vaultEntryResponse, len(entries))
	for i, e := range entries {
		resp[i] = vaultEntryResponseFromMeta(e)
	}
	writeData(w, http.StatusOK, map[string]any{"entries": resp})
}

// SetScopedVaultEntry handles PUT /api/vault/{name}.
func (s *Server) SetScopedVaultEntry(w http.ResponseWriter, r *http.Request, name string) {
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
		Value   string `json:"value"`
		Scope   string `json:"scope"`
		AgentID string `json:"agent_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}
	if body.Scope == "" {
		body.Scope = vault.ScopeUser
	}
	userID, agentID, ok := s.resolveVaultScope(w, r, info, body.Scope, body.AgentID)
	if !ok {
		return
	}
	if err := validateSpecialVaultValue(name, body.Value); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var err error
	if isSystemVaultScope(body.Scope) {
		err = s.vaultSvc.SetSystemScoped(r.Context(), body.Scope, agentID, name, body.Value)
	} else {
		err = s.vaultSvc.SetScoped(r.Context(), body.Scope, userID, agentID, name, body.Value)
	}
	if err != nil {
		s.log.Error("set scoped vault entry", "user_id", info.UserID, "scope", body.Scope, "agent_id", agentID, "name", name, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	meta, err := s.vaultSvc.GetScopedMeta(r.Context(), body.Scope, userID, agentID, name)
	if err != nil {
		s.log.Error("get scoped vault entry meta", "user_id", info.UserID, "scope", body.Scope, "agent_id", agentID, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeData(w, http.StatusOK, vaultEntryResponseFromMeta(meta))
}

// DeleteScopedVaultEntry handles DELETE /api/vault/{name}.
func (s *Server) DeleteScopedVaultEntry(w http.ResponseWriter, r *http.Request, name string, params apiserver.DeleteScopedVaultEntryParams) {
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

	scope := vault.ScopeUser
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	userID, agentID, ok := s.resolveVaultScope(w, r, info, scope, agentID)
	if !ok {
		return
	}
	var err error
	if isSystemVaultScope(scope) {
		err = s.vaultSvc.DeleteSystemScoped(r.Context(), scope, agentID, name)
	} else {
		err = s.vaultSvc.DeleteScoped(r.Context(), scope, userID, agentID, name)
	}
	if err != nil {
		s.log.Error("delete scoped vault entry", "user_id", info.UserID, "scope", scope, "agent_id", agentID, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resolveVaultScope(w http.ResponseWriter, r *http.Request, info *AuthInfo, scope string, agentID string) (string, string, bool) {
	userID := ""
	switch scope {
	case vault.ScopeUser:
		userID = info.UserID
		agentID = ""
	case vault.ScopeUserAgent:
		userID = info.UserID
		if agentID == "" {
			writeError(w, http.StatusBadRequest, "agent_id is required for user_agent scope")
			return "", "", false
		}
	case vault.ScopeSystem:
		if !info.IsAdmin {
			writeError(w, http.StatusForbidden, "admin access required")
			return "", "", false
		}
		agentID = ""
	case vault.ScopeSystemAgent:
		if !info.IsAdmin {
			writeError(w, http.StatusForbidden, "admin access required")
			return "", "", false
		}
		if agentID == "" {
			writeError(w, http.StatusBadRequest, "agent_id is required for system_agent scope")
			return "", "", false
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid scope")
		return "", "", false
	}
	if agentID != "" {
		if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
			writeError(w, code, msg)
			return "", "", false
		}
	}
	return userID, agentID, true
}

func isSystemVaultScope(scope string) bool {
	return scope == vault.ScopeSystem || scope == vault.ScopeSystemAgent
}

func vaultEntryResponseFromMeta(e vault.EntryMeta) vaultEntryResponse {
	return vaultEntryResponse{
		Name:      e.Name,
		Scope:     e.Scope,
		UserID:    e.UserID,
		AgentID:   e.AgentID,
		CreatedAt: parseTime(e.CreatedAt),
		UpdatedAt: parseTime(e.UpdatedAt),
	}
}

func validateSpecialVaultValue(name string, value string) error {
	if name != "EMAIL_CONFIG" {
		return nil
	}
	var cfg email.Config
	if err := json.Unmarshal([]byte(value), &cfg); err != nil {
		return fmt.Errorf("invalid EMAIL_CONFIG: malformed JSON")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid email config: %w", err)
	}
	return nil
}
