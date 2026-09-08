package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/mcp"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

func writeMCPError(w http.ResponseWriter, err error) {
	if errors.Is(err, authz.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, authz.ErrForbidden) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if errors.Is(err, mcp.ErrVersionConflict) || errors.Is(err, pluginpkg.ErrConflict) {
		writeError(w, http.StatusConflict, "registration changed; re-read it and retry")
		return
	}
	var duplicate *mcp.DuplicateServerError
	if errors.As(err, &duplicate) {
		writeError(w, http.StatusConflict, duplicate.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

// ListAgentMcpServers returns the effective file-backed MCP declarations for
// one agent. The projection carries only stable resource identity and public
// capability state; endpoints, payloads, credential references, and OAuth
// state remain behind the scoped MCP file APIs.
func (s *Server) ListAgentMcpServers(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	if s.mcpFiles == nil {
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
	servers, err := s.mcpFiles.Capture(ctx, authority, id)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	out := make([]apitypes.AgentMCPServer, len(servers))
	for i, server := range servers {
		out[i] = s.agentMCPServerResponse(ctx, authority, server)
	}
	writeData(w, http.StatusOK, apitypes.AgentMCPServerList{Servers: out})
}

func (s *Server) agentMCPServerResponse(ctx context.Context, authority authz.Authority, server mcp.FileServer) apitypes.AgentMCPServer {
	reg := server.Registration
	tools := make([]apitypes.MCPTool, len(reg.Tools))
	for i, tool := range reg.Tools {
		tools[i] = apitypes.MCPTool{Name: tool.Name}
		if tool.Description != "" {
			description := tool.Description
			tools[i].Description = &description
		}
		if tool.InputSchema != nil {
			schema := tool.InputSchema
			tools[i].InputSchema = &schema
		}
		if tool.Annotations != nil {
			annotations := tool.Annotations
			tools[i].Annotations = &annotations
		}
	}
	id := server.ID
	resourceID := server.Resource.Key.ID()
	name := reg.Name
	serverKey := server.ServerKey
	contentDigest := server.Resource.Digest
	needsAuth := reg.AuthType != mcp.AuthTypeNone
	if needsAuth && s.mcpSvc != nil {
		ready, err := s.mcpSvc.FileCredentialReady(ctx, reg, authority)
		needsAuth = err != nil || !ready
	}
	return apitypes.AgentMCPServer{
		Id:             &id,
		ResourceId:     &resourceID,
		Name:           &name,
		ServerKey:      &serverKey,
		ContentDigest:  &contentDigest,
		Scope:          apitypes.AgentMCPServerScope(reg.Scope),
		Enabled:        !server.Resource.Disabled && !server.Resource.Forbidden,
		AuthType:       apitypes.AgentMCPServerAuthType(reg.AuthType),
		CredentialMode: apitypes.AgentMCPServerCredentialMode(reg.CredentialMode),
		NeedsAuth:      needsAuth,
		Status:         apitypes.AgentMCPServerStatus(agentMCPStatus(reg.Status)),
		Tools:          tools,
		Readable:       true,
	}
}

func agentMCPServerResponse(server mcp.FileServer) apitypes.AgentMCPServer {
	return (&Server{}).agentMCPServerResponse(context.Background(), authz.Authority{}, server)
}

func agentMCPStatus(status string) string {
	switch status {
	case mcp.StatusOK:
		return "ready"
	case mcp.StatusNeedsAuth:
		return "needs_auth"
	case mcp.StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// mcpOAuthCallbackPath is the redirect URI path registered with authorization
// servers. It hangs off the configured base URL rather than the request origin
// because dynamic client registration persists the redirect URI once per
// registration; a per-request origin would break the next user's flow.
const mcpOAuthCallbackPath = "/api/mcp/oauth/callback"

func (s *Server) mcpOAuthCallbackURL() string {
	return strings.TrimRight(s.baseURL, "/") + mcpOAuthCallbackPath
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
