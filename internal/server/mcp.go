package server

import (
	"errors"
	"net/http"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/mcp"
)

// mcpServerResponse converts a domain registration into the API shape. It never
// exposes the credential (the bearer token stays in the vault).
func mcpServerResponse(reg mcp.Registration) apitypes.MCPServer {
	resp := apitypes.MCPServer{Id: reg.ID, Scope: apitypes.MCPServerScope(reg.Scope), Name: reg.Name, Url: reg.URL, Transport: apitypes.MCPServerTransport(reg.Transport), AuthType: apitypes.MCPServerAuthType(reg.AuthType), Enabled: reg.Enabled, Status: apitypes.MCPServerStatus(reg.Status), CredentialMode: apitypes.MCPServerCredentialMode(reg.CredentialMode)}
	version := reg.Version()
	resp.Version = &version
	if reg.StatusError != "" {
		statusError := reg.StatusError
		resp.StatusError = &statusError
	}
	if reg.AgentID != "" {
		resp.AgentId = &reg.AgentID
	}
	if reg.UserID != "" {
		resp.UserId = &reg.UserID
	}
	if !reg.ProbedAt.IsZero() {
		probed := reg.ProbedAt
		resp.ProbedAt = &probed
	}
	if len(reg.Tools) > 0 {
		tools := make([]apitypes.MCPTool, len(reg.Tools))
		for i, t := range reg.Tools {
			tool := apitypes.MCPTool{Name: t.Name}
			if t.Description != "" {
				description := t.Description
				tool.Description = &description
			}
			if t.InputSchema != nil {
				tool.InputSchema = &t.InputSchema
			}
			if t.Annotations != nil {
				tool.Annotations = &t.Annotations
			}
			tools[i] = tool
		}
		resp.Tools = &tools
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
	if errors.Is(err, mcp.ErrVersionConflict) {
		writeError(w, http.StatusConflict, "registration changed; re-read it and retry")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

// probeAfterSave best-effort probes a freshly created or edited registration:
// the save always stands, and a probe failure is persisted on the row, not
// surfaced as an HTTP error.
func (s *Server) probeAfterSave(r *http.Request, reg mcp.Registration) mcp.Registration {
	if s.mcpSvc == nil {
		return reg
	}
	probed, err := s.mcpSvc.Probe(r.Context(), reg)
	if err != nil {
		s.log.Warn("post-save mcp probe failed", "server", reg.Name, "error", err)
		return reg
	}
	return probed
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

// mcpIfMatch returns the trimmed If-Match header; malformed/empty is treated as
// absent so a conditional write is opt-in.
func mcpIfMatch(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("If-Match"))
}

func (s *Server) ProbeMCPServer(w http.ResponseWriter, r *http.Request, id string, params apiserver.ProbeMCPServerParams) {
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
	reg, err := access.Probe(r.Context(), id, scope, mcpAgentID(params.AgentId))
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			writeError(w, http.StatusForbidden, "forbidden")
		} else {
			writeError(w, http.StatusNotFound, "MCP server not found")
		}
		return
	}
	// A failed probe is a successful observation: status=error carries the reason.
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
	writeData(w, http.StatusCreated, mcpServerResponse(s.probeAfterSave(r, reg)))
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

	// Read the pre-update registration so we know whether the connection
	// surface changed and a fresh probe is warranted.
	var before mcp.Registration
	if s.mcpSvc != nil {
		if current, err := access.Get(r.Context(), id, scope, agentID); err == nil {
			before = current
		}
	}

	in := mcp.UpdateInput{ID: id, Scope: scope, AgentID: agentID, NewScope: &newScope, NewAgentID: newAgentID, Name: body.Name, URL: body.Url, Transport: transport, AuthType: authType, Enabled: body.Enabled, Token: body.Token}
	var reg mcp.Registration
	var err error
	if ifMatch := mcpIfMatch(r); ifMatch != "" {
		reg, err = access.UpdateIfVersion(r.Context(), in, ifMatch)
	} else {
		reg, err = access.Update(r.Context(), in)
	}
	if err != nil {
		writeMCPError(w, err)
		return
	}
	if before.ID != "" && (reg.URL != before.URL || reg.Transport != before.Transport || reg.AuthType != before.AuthType) {
		reg = s.probeAfterSave(r, reg)
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
	var err error
	if ifMatch := mcpIfMatch(r); ifMatch != "" {
		err = access.DeleteIfVersion(r.Context(), id, scope, mcpAgentID(params.AgentId), ifMatch)
	} else {
		err = access.Delete(r.Context(), id, scope, mcpAgentID(params.AgentId))
	}
	if err != nil {
		writeMCPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
