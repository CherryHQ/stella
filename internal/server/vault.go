package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CherryHQ/stella/internal/toolctx"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/vault"
)

// vaultEntryResponse is the JSON shape returned by ListVaultEntries.
type vaultEntryResponse struct {
	Name             string    `json:"name"`
	Description      *string   `json:"description,omitempty"`
	Scope            string    `json:"scope"`
	UserID           string    `json:"user_id,omitempty"`
	AgentID          string    `json:"agent_id,omitempty"`
	InjectAlways     bool      `json:"inject_always"`
	InjectAgentIDs   []string  `json:"inject_agent_ids"`
	InjectProjectIDs []string  `json:"inject_project_ids"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
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

// GetScopedVaultEntry handles GET /api/vault/{name}.
func (s *Server) GetScopedVaultEntry(w http.ResponseWriter, r *http.Request, name string, params apiserver.GetScopedVaultEntryParams) {
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

	value, err := s.vaultSvc.GetScoped(r.Context(), scope, userID, agentID, name)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "vault entry not found")
			return
		}
		s.log.Error("get scoped vault entry", "user_id", info.UserID, "scope", scope, "agent_id", agentID, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// A sandbox scoped token reading a secret value is the declare-time escape
	// hatch: it must leave the same audit trail as the bash `secrets` param,
	// and the read is denied if the audit row cannot be written (fail-closed).
	if boundAgentID, sessionID, ok := info.scopedBoundary(); ok {
		if err := s.vaultSvc.RecordExecSecretUse(r.Context(), info.UserID, boundAgentID, sessionID, name, "api: vault get"); err != nil {
			s.log.Error("audit vault get from sandbox", "user_id", info.UserID, "agent_id", boundAgentID, "name", name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	writeData(w, http.StatusOK, map[string]string{"name": name, "value": value})
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
		Value            string   `json:"value"`
		Description      *string  `json:"description"`
		Scope            string   `json:"scope"`
		AgentID          string   `json:"agent_id"`
		InjectAlways     *bool    `json:"inject_always"`
		InjectAgentIDs   []string `json:"inject_agent_ids"`
		InjectProjectIDs []string `json:"inject_project_ids"`
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

	opts := vault.SetOptions{
		Description:      body.Description,
		InjectAlways:     body.InjectAlways,
		InjectAgentIDs:   body.InjectAgentIDs,
		InjectProjectIDs: body.InjectProjectIDs,
		ReplaceAgents:    body.InjectAgentIDs != nil,
		ReplaceProjects:  body.InjectProjectIDs != nil,
	}
	var err error
	if isSystemVaultScope(body.Scope) {
		err = s.vaultSvc.SetSystemScopedWithOptions(r.Context(), body.Scope, agentID, name, body.Value, opts)
	} else {
		err = s.vaultSvc.SetScopedWithOptions(r.Context(), body.Scope, userID, agentID, name, body.Value, opts)
	}
	if err != nil {
		s.log.Error("set scoped vault entry", "user_id", info.UserID, "scope", body.Scope, "agent_id", agentID, "name", name, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	s.invalidateVaultRunners(body.Scope, userID, agentID, name, "set")
	meta, err := s.vaultSvc.GetScopedMeta(r.Context(), body.Scope, userID, agentID, name)
	if err != nil {
		s.log.Error("get scoped vault entry meta", "user_id", info.UserID, "scope", body.Scope, "agent_id", agentID, "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeData(w, http.StatusOK, vaultEntryResponseFromMeta(meta))
}

// ListVaultExecSecretAudit handles GET /api/vault/audit.
func (s *Server) ListVaultExecSecretAudit(w http.ResponseWriter, r *http.Request, params apiserver.ListVaultExecSecretAuditParams) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	limit := int32(20)
	if params.Limit != nil {
		limit = int32(*params.Limit)
	}
	rows, err := s.vaultSvc.ListExecSecretAudit(r.Context(), info.UserID, limit)
	if err != nil {
		s.log.Error("list vault exec secret audit", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"entries": rows})
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
	s.invalidateVaultRunners(scope, userID, agentID, name, "delete")
	w.WriteHeader(http.StatusNoContent)
}

// invalidateVaultRunners closes the live runners a vault mutation affects so the
// next session reads the new snapshot instead of the value baked into the sandbox
// env at start. Reach follows the scope: a single user for user/user_agent, one
// agent across all users for system_agent, and every runner for system (whose
// secrets merge into every agent's env via LoadEnvForAgentProject).
func (s *Server) invalidateVaultRunners(scope, userID, agentID, name, op string) {
	var err error
	switch scope {
	case vault.ScopeSystem:
		err = s.credSvc.InvalidateAll()
	case vault.ScopeSystemAgent:
		err = s.credSvc.InvalidateAgent(agentID)
	default: // user, user_agent
		if userID == "" {
			return
		}
		err = s.credSvc.InvalidateUser(userID)
	}
	if err != nil {
		s.log.Warn("invalidate runners after vault "+op, "scope", scope, "user_id", userID, "agent_id", agentID, "name", name, "error", err)
	}
}

func (s *Server) resolveVaultScope(w http.ResponseWriter, r *http.Request, info *AuthInfo, scope string, agentID string) (string, string, bool) {
	boundAgent, _, scoped := info.scopedBoundary()
	resolved, err := vault.ResolveScope(vault.ScopeRequest{
		Scope:        scope,
		UserID:       info.UserID,
		AgentID:      agentID,
		IsAdmin:      info.IsAdmin,
		AgentScoped:  scoped,
		BoundAgentID: boundAgent,
	})
	if err != nil {
		if errors.Is(err, toolctx.ErrForbidden) {
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

func isSystemVaultScope(scope string) bool {
	return scope == vault.ScopeSystem || scope == vault.ScopeSystemAgent
}

func vaultEntryResponseFromMeta(e vault.EntryMeta) vaultEntryResponse {
	var description *string
	if e.Description != "" {
		description = &e.Description
	}
	return vaultEntryResponse{
		Name:             e.Name,
		Description:      description,
		Scope:            e.Scope,
		UserID:           e.UserID,
		AgentID:          e.AgentID,
		InjectAlways:     e.InjectAlways,
		InjectAgentIDs:   e.InjectAgentIDs,
		InjectProjectIDs: e.InjectProjectIDs,
		CreatedAt:        parseTime(e.CreatedAt),
		UpdatedAt:        parseTime(e.UpdatedAt),
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
