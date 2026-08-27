package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/mcp"
)

// mcpServerResponse converts a domain registration into the API shape. It never
// exposes the credential (the bearer token stays in the vault).
func mcpServerResponse(reg mcp.Registration) apitypes.MCPServer {
	resp := apitypes.MCPServer{
		Id:        reg.ID,
		Scope:     apitypes.MCPServerScope(reg.Scope),
		Name:      reg.Name,
		Url:       reg.URL,
		Transport: apitypes.MCPServerTransport(reg.Transport),
		AuthType:  apitypes.MCPServerAuthType(reg.AuthType),
		Enabled:   reg.Enabled,
	}
	if reg.UserID != "" {
		resp.UserId = &reg.UserID
	}
	if reg.AgentID != "" {
		resp.AgentId = &reg.AgentID
	}
	created := reg.CreatedAt
	updated := reg.UpdatedAt
	resp.CreatedAt = &created
	resp.UpdatedAt = &updated
	return resp
}

// ListScopedMCPServers handles GET /api/mcp/servers.
func (s *Server) ListScopedMCPServers(w http.ResponseWriter, r *http.Request, params apiserver.ListScopedMCPServersParams) {
	if s.mcpSvc == nil || s.mcpAccess == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	scope := mcp.ScopeUser
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	access, err := s.mcpAccess.Begin(authority)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	regs, err := access.List(r.Context(), scope, agentID)
	if err != nil {
		s.log.Error("list scoped mcp servers", "user_id", info.UserID, "scope", scope, "agent_id", agentID, "error", err)
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	out := make([]apitypes.MCPServer, len(regs))
	for i, reg := range regs {
		out[i] = mcpServerResponse(reg)
	}
	writeData(w, http.StatusOK, apitypes.MCPServerList{Servers: out})
}

// CreateScopedMCPServer handles POST /api/mcp/servers.
func (s *Server) CreateScopedMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.mcpSvc == nil || s.mcpAccess == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var body apitypes.CreateMCPServerRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	scope := mcp.ScopeUser
	if body.Scope != "" {
		scope = string(body.Scope)
	}
	agentID := ""
	if body.AgentId != nil {
		agentID = *body.AgentId
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	access, err := s.mcpAccess.Begin(authority)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	transport := mcp.TransportStreamableHTTP
	if body.Transport != nil {
		transport = string(*body.Transport)
	}
	authType := mcp.AuthTypeNone
	if body.AuthType != nil {
		authType = string(*body.AuthType)
	}
	token := ""
	if body.Token != nil {
		token = *body.Token
	}

	reg, err := access.Create(r.Context(), mcp.CreateInput{
		Scope:     scope,
		AgentID:   agentID,
		Name:      body.Name,
		URL:       body.Url,
		Transport: transport,
		AuthType:  authType,
		Token:     token,
	})
	if err != nil {
		// Validation errors (bad transport such as stdio, missing token, etc.) are
		// client errors, not server faults.
		s.log.Warn("create scoped mcp server", "user_id", info.UserID, "scope", scope, "agent_id", agentID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, mcpServerResponse(reg))
}

// UpdateScopedMCPServer handles PATCH /api/mcp/servers/{id}.
func (s *Server) UpdateScopedMCPServer(w http.ResponseWriter, r *http.Request, id string, params apiserver.UpdateScopedMCPServerParams) {
	if s.mcpSvc == nil || s.mcpAccess == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	var body apitypes.UpdateMCPServerRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	scope := mcp.ScopeUser
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	access, err := s.mcpAccess.Begin(authority)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	newScope := scope
	if body.Scope != nil {
		newScope = string(*body.Scope)
	}
	newAgentID := agentID
	if body.AgentId != nil {
		newAgentID = *body.AgentId
	}
	// Access resolves both the current and destination owner from Authority.
	newUserID := ""

	var transport *string
	if body.Transport != nil {
		v := string(*body.Transport)
		transport = &v
	}
	var authType *string
	if body.AuthType != nil {
		v := string(*body.AuthType)
		authType = &v
	}

	reg, err := access.Update(r.Context(), mcp.UpdateInput{
		ID:         id,
		Scope:      scope,
		AgentID:    agentID,
		NewScope:   &newScope,
		NewUserID:  newUserID,
		NewAgentID: newAgentID,
		Name:       body.Name,
		URL:        body.Url,
		Transport:  transport,
		AuthType:   authType,
		Enabled:    body.Enabled,
		Token:      body.Token,
	})
	if err != nil {
		s.log.Warn("update scoped mcp server", "user_id", info.UserID, "scope", scope, "agent_id", agentID, "id", id, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, mcpServerResponse(reg))
}

// DeleteScopedMCPServer handles DELETE /api/mcp/servers/{id}.
func (s *Server) DeleteScopedMCPServer(w http.ResponseWriter, r *http.Request, id string, params apiserver.DeleteScopedMCPServerParams) {
	if s.mcpSvc == nil || s.mcpAccess == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	scope := mcp.ScopeUser
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	access, err := s.mcpAccess.Begin(authority)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := access.Delete(r.Context(), id, scope, agentID); err != nil {
		s.log.Error("delete scoped mcp server", "user_id", info.UserID, "scope", scope, "agent_id", agentID, "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
