package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/plugin"
)

func (s *Server) beginMCPFiles(w http.ResponseWriter, r *http.Request) (*mcp.FileAccess, authz.Authority, bool) {
	if s.mcpFiles == nil {
		writeCapabilityUnavailable(w, capMCP)
		return nil, authz.Authority{}, false
	}
	info := requireAuth(w, r)
	if info == nil {
		return nil, authz.Authority{}, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, authz.Authority{}, false
	}
	access, err := s.mcpFiles.Begin(authority)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, authz.Authority{}, false
	}
	return access, authority, true
}

func (s *Server) ListMCPServers(w http.ResponseWriter, r *http.Request, params apiserver.ListMCPServersParams) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	var scope *plugin.Scope
	if params.Scope != nil {
		value := plugin.Scope(*params.Scope)
		scope = &value
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	items, err := files.List(r.Context(), scope, agentID)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	if offset > len(items) {
		writeError(w, http.StatusBadRequest, "invalid page_token")
		return
	}
	items = items[offset:]
	page, next := nextPageTokenForRows(items, limit, offset)
	truncated := next != ""
	result := make([]apitypes.MCPServer, len(page))
	for i := range page {
		result[i] = s.fileMCPView(r.Context(), files, authority, page[i])
	}
	writeData(w, http.StatusOK, apitypes.MCPServerList{Servers: result, NextPageToken: stringPtrOrNil(next), Truncated: &truncated})
}

func (s *Server) CreateMCPServer(w http.ResponseWriter, r *http.Request) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	var req apitypes.CreateMCPServerRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	declaration, err := mcpDeclarationFromAPI(req.Declaration)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := ""
	if req.AgentId != nil {
		agentID = *req.AgentId
	}
	if agentID == "" && authority.Kind() == authz.ActorAgent && (req.Scope == apitypes.CreateMCPServerRequestScopeUserAgent || req.Scope == apitypes.CreateMCPServerRequestScopeSystemAgent) {
		agentID = string(authority.AgentID())
	}
	item, err := files.Create(r.Context(), plugin.Scope(req.Scope), agentID, req.Name, declaration)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	writeData(w, http.StatusCreated, s.fileMCPView(r.Context(), files, authority, item))
}

func (s *Server) GetMCPServer(w http.ResponseWriter, r *http.Request, id string) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	item, err := files.Get(r.Context(), id)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.fileMCPView(r.Context(), files, authority, item))
}

func (s *Server) UpdateMCPServer(w http.ResponseWriter, r *http.Request, id string) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedDigest         string                   `json:"expected_digest,omitempty"`
		Declaration            *apitypes.MCPDeclaration `json:"declaration,omitempty"`
		IsEnabled              *bool                    `json:"is_enabled,omitempty"`
		ExpectedSettingsDigest *string                  `json:"expected_settings_digest,omitempty"`
	}
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	settingsUpdate := req.IsEnabled != nil || req.ExpectedSettingsDigest != nil
	if req.Declaration != nil || req.ExpectedDigest != "" {
		if req.Declaration == nil || req.ExpectedDigest == "" || settingsUpdate {
			writeError(w, http.StatusBadRequest, "declaration updates require declaration and expected_digest only")
			return
		}
		declaration, err := mcpDeclarationFromAPI(*req.Declaration)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item, err := files.UpdateDeclaration(r.Context(), id, req.ExpectedDigest, declaration)
		if err != nil {
			writeMCPFileError(w, err)
			return
		}
		writeData(w, http.StatusOK, s.fileMCPView(r.Context(), files, authority, item))
		return
	}
	if req.IsEnabled == nil || req.ExpectedSettingsDigest == nil {
		writeError(w, http.StatusBadRequest, "enablement updates require is_enabled and expected_settings_digest")
		return
	}
	item, err := files.SetEnabled(r.Context(), id, *req.ExpectedSettingsDigest, *req.IsEnabled)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.fileMCPView(r.Context(), files, authority, item))
}

func (s *Server) DeleteMCPServer(w http.ResponseWriter, r *http.Request, id string, params apiserver.DeleteMCPServerParams) {
	files, _, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	if err := files.Delete(r.Context(), id, params.ExpectedDigest); err != nil {
		writeMCPFileError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) GetMCPServerFile(w http.ResponseWriter, r *http.Request, id string) {
	files, _, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	data, digest, err := files.ReadDeclaration(r.Context(), id)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	writeData(w, http.StatusOK, apitypes.ResourceFileContent{ContentBase64: data, Path: "mcp.json", ResourceDigest: &digest})
}

func (s *Server) UpdateMCPServerFile(w http.ResponseWriter, r *http.Request, id string) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	var req apitypes.UpdateMCPServerFileRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	item, err := files.WriteDeclaration(r.Context(), id, req.ExpectedDigest, req.ContentBase64)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.fileMCPView(r.Context(), files, authority, item))
}

func (s *Server) UpdateMCPServerCredentials(w http.ResponseWriter, r *http.Request, id string) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	var req apitypes.UpdateMCPServerCredentialsRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	bearer, secret := "", ""
	if req.BearerToken != nil {
		bearer = *req.BearerToken
	}
	if req.ClientSecret != nil {
		secret = *req.ClientSecret
	}
	if (bearer == "") == (secret == "") {
		writeError(w, http.StatusBadRequest, "supply exactly one credential")
		return
	}
	item, err := files.SetCredentials(r.Context(), id, req.ExpectedDigest, bearer, secret)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.fileMCPView(r.Context(), files, authority, item))
}

func (s *Server) ProbeMCPServer(w http.ResponseWriter, r *http.Request, id string) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	if s.mcpSvc == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	item, err := files.Get(r.Context(), id)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	if item.Resource.Disabled || item.Resource.Forbidden {
		writeMCPFileError(w, plugin.ErrForbidden)
		return
	}
	probed, err := s.mcpSvc.ProbeFile(r.Context(), item.Registration, authority)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	item.Registration = probed
	writeData(w, http.StatusOK, s.fileMCPView(r.Context(), files, authority, item))
}

func (s *Server) StartMCPServerOAuth(w http.ResponseWriter, r *http.Request, id string) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	var req apiserver.StartMCPServerOAuthJSONBody
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	item, err := files.Get(r.Context(), id)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	if item.Resource.Digest != req.ExpectedDigest {
		writeMCPFileError(w, plugin.ErrConflict)
		return
	}
	authURL, flowID, expiresAt, err := s.mcpSvc.StartOAuthForAuthority(r.Context(), item.Registration, authority, s.mcpOAuthCallbackURL())
	if err != nil {
		writePluginOAuthError(w, err)
		return
	}
	writeData(w, http.StatusCreated, apitypes.MCPOAuthStart{AuthorizationUrl: authURL, FlowId: flowID, ExpiresAt: expiresAt.UTC()})
}

func (s *Server) DisconnectMCPServerOAuth(w http.ResponseWriter, r *http.Request, id string) {
	files, authority, ok := s.beginMCPFiles(w, r)
	if !ok {
		return
	}
	item, err := files.Get(r.Context(), id)
	if err != nil {
		writeMCPFileError(w, err)
		return
	}
	if err := s.mcpSvc.DisconnectFile(r.Context(), item.Registration, authority); err != nil {
		writePluginOAuthError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.fileMCPView(r.Context(), files, authority, item))
}

func mcpDeclarationFromAPI(in apitypes.MCPDeclaration) (mcpconfig.Declaration, error) {
	d := mcpconfig.Declaration{URL: in.Url, Transport: string(in.Transport), Authentication: mcpconfig.Authentication{Type: string(in.AuthType), Mode: string(in.CredentialMode)}}
	if in.Description != nil {
		d.Description = *in.Description
	}
	if in.Headers != nil {
		d.Headers = *in.Headers
	}
	if in.CallTimeoutSeconds != nil {
		d.CallTimeoutSeconds = *in.CallTimeoutSeconds
	}
	if in.CredentialRef != nil {
		d.CredentialRef = *in.CredentialRef
	}
	if in.ClientId != nil {
		d.ClientID = *in.ClientId
	}
	if in.ClientSecretRef != nil {
		d.ClientSecretRef = *in.ClientSecretRef
	}
	if in.TokenEndpointAuthMethod != nil {
		d.TokenEndpointAuthMethod = string(*in.TokenEndpointAuthMethod)
	}
	if in.Scopes != nil {
		d.Scopes = append([]string(nil), (*in.Scopes)...)
	}
	return mcpconfig.Normalize(d)
}

func (s *Server) fileMCPView(ctx context.Context, files *mcp.FileAccess, authority authz.Authority, item mcp.FileServer) apitypes.MCPServer {
	reg := item.Registration
	id, resourceID := item.ID, item.Resource.Key.ID()
	name, key := reg.Name, item.ServerKey
	contentDigest, settingsDigest := item.Resource.Digest, item.Resource.SettingsDigest
	isReadOnly := item.Resource.Key.Kind == plugin.ResourcePlugin
	if files != nil {
		isReadOnly = !files.CanWrite(ctx, item)
	}
	isStandalone := item.Resource.Key.Kind == plugin.ResourceMCP
	isOverridden := item.Resource.Overridden
	enabled := !item.Resource.Disabled && !item.Resource.Forbidden
	needsAuth := reg.AuthType != mcp.AuthTypeNone
	if needsAuth && s.mcpSvc != nil {
		ready, err := s.mcpSvc.FileCredentialReady(ctx, reg, authority)
		needsAuth = err != nil || !ready
	}
	view := apitypes.MCPServer{Id: &id, ResourceId: &resourceID, Name: &name, ServerKey: &key, Scope: apitypes.MCPServerScope(reg.Scope), IsEnabled: enabled, IsReadOnly: &isReadOnly, IsStandalone: &isStandalone, IsOverridden: &isOverridden, ContentDigest: &contentDigest, SettingsDigest: &settingsDigest, NeedsAuth: needsAuth, Status: mcpServerStatus(reg.Status), Tools: make([]apitypes.MCPTool, len(reg.Tools))}
	view.Diagnostics = pluginDiagnostics(item.Resource.Diagnostics)
	if reg.UserID != "" {
		if userID, err := uuid.Parse(reg.UserID); err == nil {
			view.UserId = &userID
		}
	}
	if reg.AgentID != "" {
		view.AgentId = &reg.AgentID
	}
	if reg.StatusError != "" {
		view.StatusError = &reg.StatusError
	}
	if declaration, ok := item.Resource.MCP[item.ServerKey]; ok {
		apiDeclaration := mcpDeclarationToAPI(declaration)
		view.Declaration = &apiDeclaration
	}
	for i, tool := range reg.Tools {
		view.Tools[i] = apitypes.MCPTool{Name: tool.Name}
		if tool.Description != "" {
			view.Tools[i].Description = &tool.Description
		}
	}
	return view
}

// mcpFileView keeps the pure projection available to package tests. HTTP
// handlers use fileMCPView so write authority and credential readiness are
// evaluated with the request's bound capability.
func mcpFileView(item mcp.FileServer) apitypes.MCPServer {
	return (&Server{}).fileMCPView(context.Background(), nil, authz.Authority{}, item)
}

func mcpDeclarationToAPI(in mcpconfig.Declaration) apitypes.MCPDeclaration {
	transport := apitypes.MCPDeclarationTransport(in.Transport)
	authType := apitypes.MCPDeclarationAuthType(in.Type)
	mode := apitypes.MCPDeclarationCredentialMode(in.Mode)
	out := apitypes.MCPDeclaration{Url: in.URL, Transport: transport, AuthType: authType, CredentialMode: mode}
	if in.Description != "" {
		out.Description = &in.Description
	}
	if len(in.Headers) != 0 {
		headers := in.Headers
		out.Headers = &headers
	}
	if in.CallTimeoutSeconds != 0 {
		out.CallTimeoutSeconds = &in.CallTimeoutSeconds
	}
	if in.CredentialRef != "" {
		out.CredentialRef = &in.CredentialRef
	}
	if in.ClientID != "" {
		out.ClientId = &in.ClientID
	}
	if in.ClientSecretRef != "" {
		out.ClientSecretRef = &in.ClientSecretRef
	}
	if in.TokenEndpointAuthMethod != "" {
		method := apitypes.MCPDeclarationTokenEndpointAuthMethod(in.TokenEndpointAuthMethod)
		out.TokenEndpointAuthMethod = &method
	}
	if len(in.Scopes) != 0 {
		scopes := append([]string(nil), in.Scopes...)
		out.Scopes = &scopes
	}
	return out
}

func mcpServerStatus(status string) apitypes.MCPServerStatus {
	switch status {
	case mcp.StatusOK:
		return apitypes.MCPServerStatusReady
	case mcp.StatusNeedsAuth:
		return apitypes.MCPServerStatusNeedsAuth
	case mcp.StatusError:
		return apitypes.MCPServerStatusError
	default:
		return apitypes.MCPServerStatusUnknown
	}
}

func writeMCPFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrNotFound), errors.Is(err, plugin.ErrNotFound), errors.Is(err, agentaccess.ErrNotFound), errors.Is(err, agentaccess.ErrForbidden):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, authz.ErrForbidden), errors.Is(err, plugin.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, plugin.ErrConflict), errors.Is(err, mcp.ErrVersionConflict):
		writeError(w, http.StatusConflict, "resource changed; re-read it and retry")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func decodeStrictJSON(r *http.Request, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("request body too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body contains multiple JSON values")
		}
		return err
	}
	return nil
}
