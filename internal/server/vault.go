package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/CherryHQ/stella/internal/authz"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/vault"
)

// vaultEntryResponse is the JSON shape returned by ListVaultEntries.
type vaultEntryResponse struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Scope       string  `json:"scope"`
	UserID      string  `json:"user_id,omitempty"`
	AgentID     string  `json:"agent_id,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// ListScopedVaultEntries handles GET /api/vault.
func (s *Server) ListScopedVaultEntries(w http.ResponseWriter, r *http.Request, params apiserver.ListScopedVaultEntriesParams) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
		return
	}
	acc, ok := s.vaultAccess(w, r)
	if !ok {
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

	entries, err := acc.ListScoped(r.Context(), scope, agentID)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	resp := make([]vaultEntryResponse, len(entries))
	for i, e := range entries {
		resp[i] = vaultEntryResponseFromMeta(e)
	}
	writeData(w, http.StatusOK, map[string]any{"entries": resp})
}

// GetScopedVaultEntry handles GET /api/vault/{name}.
func (s *Server) GetScopedVaultEntry(w http.ResponseWriter, r *http.Request, name string, params apiserver.GetScopedVaultEntryParams) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
		return
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	acc, ok := s.vaultAccess(w, r)
	if !ok {
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

	value, err := acc.GetScoped(r.Context(), scope, agentID, name)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"name": name, "value": value})
}

// SetScopedVaultEntry handles PUT /api/vault/{name}.
func (s *Server) SetScopedVaultEntry(w http.ResponseWriter, r *http.Request, name string) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
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
		Value       string  `json:"value"`
		Description *string `json:"description"`
		Scope       string  `json:"scope"`
		AgentID     string  `json:"agent_id"`
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
	if err := validateSpecialVaultValue(name, body.Value); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.vaultSvc.ValidateUserFacingName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	acc, ok := s.vaultAccess(w, r)
	if !ok {
		return
	}

	opts := vault.SetOptions{Description: body.Description}
	if err := acc.SetScoped(r.Context(), body.Scope, body.AgentID, name, body.Value, opts); err != nil {
		writeVaultError(w, err)
		return
	}
	s.invalidateVaultRunners(body.Scope, info.UserID, body.AgentID, name, "set")
	meta, err := acc.GetScopedMeta(r.Context(), body.Scope, body.AgentID, name)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	writeData(w, http.StatusOK, vaultEntryResponseFromMeta(meta))
}

// DeleteScopedVaultEntry handles DELETE /api/vault/{name}.
func (s *Server) DeleteScopedVaultEntry(w http.ResponseWriter, r *http.Request, name string, params apiserver.DeleteScopedVaultEntryParams) {
	if s.vaultSvc == nil {
		writeCapabilityUnavailable(w, capVault)
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
	acc, ok := s.vaultAccess(w, r)
	if !ok {
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
	if err := acc.DeleteScoped(r.Context(), scope, agentID, name); err != nil {
		writeVaultError(w, err)
		return
	}
	s.invalidateVaultRunners(scope, info.UserID, agentID, name, "delete")
	w.WriteHeader(http.StatusNoContent)
}

// invalidateVaultRunners logs runner-cache invalidation failures without
// failing the already-committed vault mutation.
func (s *Server) invalidateVaultRunners(scope, userID, agentID, name, op string) {
	if err := vault.InvalidateForScope(s.credSvc, scope, userID, agentID); err != nil {
		s.log.Warn("invalidate runners after vault "+op, "scope", scope, "user_id", userID, "agent_id", agentID, "name", name, "error", err)
	}
}

// vaultAccess derives the trusted Authority for the authenticated caller and
// opens one vault Authorizer evaluation. The vault Service is the sole PEP: it
// decides the scope (including admin-only system scopes) and folds the agent-read
// gate, so the handler no longer resolves scope or checks IsAdmin here.
func (s *Server) vaultAccess(w http.ResponseWriter, r *http.Request) (*vault.Access, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	acc, err := s.vaultSvc.Begin(r.Context(), authority)
	if err != nil {
		writeVaultError(w, err)
		return nil, false
	}
	return acc, true
}

// resolveScope resolves the durable owner columns for a vault-style scope for
// resources OTHER than vault entries (agent tool overrides, MCP connection
// tokens) that reuse the vault scope vocabulary. Vault entries themselves are
// authorized through the ResourceVault PEP (vaultAccess), not here.
func (s *Server) resolveScope(w http.ResponseWriter, r *http.Request, info *AuthInfo, scope string, agentID string) (string, string, bool) {
	resolved, err := vault.ResolveScope(vault.ScopeRequest{
		Scope:   scope,
		UserID:  info.UserID,
		AgentID: agentID,
		IsAdmin: info.IsAdmin,
	})
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return "", "", false
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return "", "", false
	}
	if resolved.AgentID != "" {
		if _, code, msg := s.requireAgentAccess(r.Context(), resolved.AgentID); code != 0 {
			writeError(w, code, msg)
			return "", "", false
		}
	}
	return resolved.UserID, resolved.AgentID, true
}

// writeVaultError maps a vault Access error to the HTTP status, preserving the
// accepted 401/403/404 split (a denied system scope or agent mismatch is 403; a
// missing entry or hidden agent is 404).
func writeVaultError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "not authenticated")
	case errors.Is(err, authz.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, authz.ErrNotFound) || isNotFound(err):
		writeError(w, http.StatusNotFound, "vault entry not found")
	default:
		writeError(w, http.StatusBadRequest, "invalid request")
	}
}

func vaultEntryResponseFromMeta(e vault.EntryMeta) vaultEntryResponse {
	var description *string
	if e.Description != "" {
		description = &e.Description
	}
	return vaultEntryResponse{
		Name:        e.Name,
		Description: description,
		Scope:       e.Scope,
		UserID:      e.UserID,
		AgentID:     e.AgentID,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
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
