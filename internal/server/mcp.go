package server

import (
	"errors"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/mcp"
)

// mcpServerResponse converts a domain registration into the API shape. It never
// exposes the credential (the bearer token stays in the vault).
func mcpServerResponse(reg mcp.Registration) apitypes.MCPServer {
	resp := apitypes.MCPServer{Id: reg.ID, Scope: apitypes.MCPServerScope(reg.Scope), Name: reg.Name, Url: reg.URL, Transport: apitypes.MCPServerTransport(reg.Transport), AuthType: apitypes.MCPServerAuthType(reg.AuthType), Enabled: reg.Enabled}
	if reg.UserID != "" {
		resp.UserId = &reg.UserID
	}
	if reg.AgentID != "" {
		resp.AgentId = &reg.AgentID
	}
	created, updated := reg.CreatedAt, reg.UpdatedAt
	resp.CreatedAt = &created
	resp.UpdatedAt = &updated
	return resp
}

func (s *Server) beginMCPAccess(w http.ResponseWriter, r *http.Request) (*mcp.Access, *AuthInfo, bool) {
	if s.mcpSvc == nil || s.mcpAccess == nil {
		writeCapabilityUnavailable(w, capMCP)
		return nil, nil, false
	}
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, nil, false
	}
	access, err := s.mcpAccess.Begin(authority)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, nil, false
	}
	return access, info, true
}

func mcpAgentID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func writeMCPError(w http.ResponseWriter, err error) {
	if errors.Is(err, authz.ErrForbidden) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (s *Server) ListScopedMCPServers(w http.ResponseWriter, r *http.Request, params apiserver.ListScopedMCPServersParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	scope, agentID := mcp.ScopeUser, mcpAgentID(params.AgentId)
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	regs, err := access.List(r.Context(), scope, agentID)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	out := make([]apitypes.MCPServer, len(regs))
	for i, reg := range regs {
		out[i] = mcpServerResponse(reg)
	}
	writeData(w, http.StatusOK, apitypes.MCPServerList{Servers: out})
}

func (s *Server) GetScopedMCPServer(w http.ResponseWriter, r *http.Request, id string, params apiserver.GetScopedMCPServerParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	scope := mcp.ScopeUser
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	reg, err := access.Get(r.Context(), id, scope, mcpAgentID(params.AgentId))
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			writeError(w, http.StatusForbidden, "forbidden")
		} else {
			writeError(w, http.StatusNotFound, "MCP server not found")
		}
		return
	}
	writeData(w, http.StatusOK, mcpServerResponse(reg))
}

func (s *Server) CreateScopedMCPServer(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
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
	agentID := mcpAgentID(body.AgentId)
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
	reg, err := access.Create(r.Context(), mcp.CreateInput{Scope: scope, AgentID: agentID, Name: body.Name, URL: body.Url, Transport: transport, AuthType: authType, Token: token})
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusCreated, mcpServerResponse(reg))
}

func (s *Server) UpdateScopedMCPServer(w http.ResponseWriter, r *http.Request, id string, params apiserver.UpdateScopedMCPServerParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
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
	scope, agentID := mcp.ScopeUser, mcpAgentID(params.AgentId)
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	newScope := scope
	if body.Scope != nil {
		newScope = string(*body.Scope)
	}
	newAgentID := agentID
	if body.AgentId != nil {
		newAgentID = *body.AgentId
	}
	var transport, authType *string
	if body.Transport != nil {
		v := string(*body.Transport)
		transport = &v
	}
	if body.AuthType != nil {
		v := string(*body.AuthType)
		authType = &v
	}
	reg, err := access.Update(r.Context(), mcp.UpdateInput{ID: id, Scope: scope, AgentID: agentID, NewScope: &newScope, NewAgentID: newAgentID, Name: body.Name, URL: body.Url, Transport: transport, AuthType: authType, Enabled: body.Enabled, Token: body.Token})
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusOK, mcpServerResponse(reg))
}

func (s *Server) DeleteScopedMCPServer(w http.ResponseWriter, r *http.Request, id string, params apiserver.DeleteScopedMCPServerParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
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
	if err := access.Delete(r.Context(), id, scope, mcpAgentID(params.AgentId)); err != nil {
		writeMCPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
