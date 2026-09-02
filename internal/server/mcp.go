package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	info := UserFromContext(r.Context())
	userID := ""
	if info != nil {
		userID = info.UserID
	}
	probed, err := s.mcpSvc.Probe(r.Context(), reg, s.mcpSvc.CredentialOwner(reg, userID))
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
	userID := mcpCallerUserID(r)
	for i, reg := range regs {
		out[i] = s.withMCPOAuthState(r.Context(), mcpServerResponse(reg), reg, userID)
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
	writeData(w, http.StatusOK, s.withMCPOAuthState(r.Context(), mcpServerResponse(reg), reg, mcpCallerUserID(r)))
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
	writeData(w, http.StatusOK, s.withMCPOAuthState(r.Context(), mcpServerResponse(reg), reg, mcpCallerUserID(r)))
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
	probed := s.probeAfterSave(r, reg)
	writeData(w, http.StatusCreated, s.withMCPOAuthState(r.Context(), mcpServerResponse(probed), probed, mcpCallerUserID(r)))
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
	// A replaced token is a connection-surface change too: it is how a
	// needs_auth server gets repaired, so the row must not stay stale.
	if before.ID != "" && (reg.URL != before.URL || reg.Transport != before.Transport || reg.AuthType != before.AuthType || body.Token != nil) {
		reg = s.probeAfterSave(r, reg)
	}
	writeData(w, http.StatusOK, s.withMCPOAuthState(r.Context(), mcpServerResponse(reg), reg, mcpCallerUserID(r)))
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

// ListAgentMcpServers returns the MCP registrations effective for one agent
// after name-precedence dedup, with provenance for the UI: which scopes lost
// to each winner, and whether the caller can manage the row at all.
func (s *Server) ListAgentMcpServers(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	if s.mcpSvc == nil || s.mcpAccess == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	if _, code, msg := s.requireAgentAccess(ctx, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	info := UserFromContext(ctx)
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
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
	resolved, err := s.mcpSvc.ResolveForContextWithShadowed(ctx, info.UserID, id)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	out := make([]apitypes.AgentMCPServer, len(resolved))
	for i, resolvedReg := range resolved {
		// The generated AgentMCPServer flattens the allOf, so reuse the shared
		// MCPServer projection through its identical JSON shape.
		raw, err := json.Marshal(mcpServerResponse(resolvedReg.Registration))
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		var item apitypes.AgentMCPServer
		if err := json.Unmarshal(raw, &item); err != nil {
			s.writeInternalError(w, err)
			return
		}
		item.Oauth = mcpOAuthProjection(s.mcpSvc.OAuthState(ctx, resolvedReg.Registration, info.UserID))
		item.Readable = access.CanRead(ctx, resolvedReg.Registration)
		if len(resolvedReg.ShadowedScopes) > 0 {
			shadows := make([]apitypes.AgentMCPServerShadowedScopes, len(resolvedReg.ShadowedScopes))
			for j, scope := range resolvedReg.ShadowedScopes {
				shadows[j] = apitypes.AgentMCPServerShadowedScopes(scope)
			}
			item.ShadowedScopes = &shadows
		}
		out[i] = item
	}
	writeData(w, http.StatusOK, apitypes.AgentMCPServerList{Servers: out})
}

// mcpOAuthCallbackPath is the redirect URI path registered with authorization
// servers. It hangs off the configured base URL rather than the request origin
// because dynamic client registration persists the redirect URI once per
// registration; a per-request origin would break the next user's flow.
const mcpOAuthCallbackPath = "/api/mcp/oauth/callback"

func (s *Server) mcpOAuthCallbackURL() string {
	return strings.TrimRight(s.baseURL, "/") + mcpOAuthCallbackPath
}

// StartMCPOAuth handles POST /api/mcp/servers/{id}/oauth-start. The Access
// PEP decides authority: owner for shared, visibility for per_user.
func (s *Server) StartMCPOAuth(w http.ResponseWriter, r *http.Request, id string, params apiserver.StartMCPOAuthParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	_, authURL, flowID, expiresAt, err := access.StartOAuth(r.Context(), id, s.mcpOAuthCallbackURL())
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusCreated, apitypes.MCPOAuthStart{AuthorizationUrl: authURL, FlowId: flowID, ExpiresAt: expiresAt})
}

// DisconnectMCPOAuth handles POST /api/mcp/servers/{id}/oauth-disconnect.
func (s *Server) DisconnectMCPOAuth(w http.ResponseWriter, r *http.Request, id string, params apiserver.DisconnectMCPOAuthParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	reg, err := access.Disconnect(r.Context(), id, mcp.ScopeUser, "")
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.withMCPOAuthState(r.Context(), mcpServerResponse(reg), reg, mcpCallerUserID(r)))
}

// McpOAuthCallback handles GET /api/mcp/oauth/callback. It is unauthenticated:
// consuming the flow row via state re-identifies the initiating user.
func (s *Server) McpOAuthCallback(w http.ResponseWriter, r *http.Request, params apiserver.McpOAuthCallbackParams) {
	if s.mcpSvc == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	if params.Code == "" || params.State == "" {
		http.Redirect(w, r, "/settings/mcp?oauth_error=invalid_request", http.StatusFound)
		return
	}
	reg, err := s.mcpSvc.CompleteOAuth(r.Context(), params.State, params.Code)
	if err != nil {
		s.log.Warn("mcp oauth callback failed", "error", err)
		http.Redirect(w, r, "/settings/mcp?oauth_error="+mcpOAuthErrorSlug(err), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/settings/mcp?connected="+url.QueryEscape(reg.ID), http.StatusFound)
}

// mcpOAuthErrorSlug maps a callback failure to a fixed enum for the redirect
// URL. Provider error text is never echoed into the URL.
func mcpOAuthErrorSlug(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unknown, expired, or already used"):
		return "expired"
	case strings.Contains(msg, "exchange authorization code"):
		return "exchange_failed"
	default:
		return "internal"
	}
}

// mcpOAuthProjection renders the OAuth view for one registration. The unnamed
// struct type is identical to the generated MCPServer.oauth / AgentMCPServer
// oauth fields, so the result assigns into both.
func mcpOAuthProjection(st mcp.OAuthState) *struct {
	AccessExpiresAt  *time.Time `json:"access_expires_at,omitempty"`
	ClientRegistered bool       `json:"client_registered"`
	Connected        bool       `json:"connected"`
	NeedsReconnect   bool       `json:"needs_reconnect"`
} {
	out := &struct {
		AccessExpiresAt  *time.Time `json:"access_expires_at,omitempty"`
		ClientRegistered bool       `json:"client_registered"`
		Connected        bool       `json:"connected"`
		NeedsReconnect   bool       `json:"needs_reconnect"`
	}{ClientRegistered: st.ClientRegistered, Connected: st.Connected, NeedsReconnect: st.NeedsReconnect}
	if !st.AccessExpiresAt.IsZero() {
		expires := st.AccessExpiresAt
		out.AccessExpiresAt = &expires
	}
	return out
}

// withMCPOAuthState attaches the calling user's OAuth view to an MCPServer
// response. Token material never leaves the vault; only booleans and an expiry.
func (s *Server) withMCPOAuthState(ctx context.Context, resp apitypes.MCPServer, reg mcp.Registration, userID string) apitypes.MCPServer {
	if s.mcpSvc == nil || reg.AuthType != mcp.AuthTypeOAuth {
		return resp
	}
	resp.Oauth = mcpOAuthProjection(s.mcpSvc.OAuthState(ctx, reg, userID))
	return resp
}

// mcpCallerUserID returns the authenticated user id for OAuth-state projection.
func mcpCallerUserID(r *http.Request) string {
	if info := UserFromContext(r.Context()); info != nil {
		return info.UserID
	}
	return ""
}
