package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/mcp"
)

func (s *Server) ListMCPServers(w http.ResponseWriter, r *http.Request, params apiserver.ListMCPServersParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	regs, err := access.ListChildren(r.Context(), params.ParentConfigId.String())
	if err != nil {
		writeMCPError(w, err)
		return
	}
	items := make([]apitypes.MCPServer, len(regs))
	for i := range regs {
		items[i] = mcpServerView(regs[i])
	}
	writeData(w, http.StatusOK, apitypes.MCPServerList{Servers: items, NextPageToken: nil})
}

func (s *Server) CreateMCPServer(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	var req apitypes.CreateMCPServerRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	in := mcp.CreateInput{URL: req.Url, Transport: stringPtrValue(req.Transport), AuthType: stringPtrValue(req.AuthType), CredentialMode: stringPtrValue(req.CredentialMode)}
	if req.Metadata != nil {
		in.Metadata = *req.Metadata
	}
	if req.Credentials != nil {
		if v, ok := (*req.Credentials)["token"].(string); ok {
			in.Token = v
		}
		if v, ok := (*req.Credentials)["oauth_client_id"].(string); ok {
			in.OAuthClientID = v
		}
		if v, ok := (*req.Credentials)["oauth_client_secret"].(string); ok {
			in.OAuthClientSecret = v
		}
	}
	result, err := access.CreateChild(r.Context(), req.ParentConfigId.String(), req.ServerKey, req.ExpectedParentRevision, in)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusCreated, mcpServerView(result))
}

func (s *Server) GetMCPServer(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	reg, err := access.GetVisible(r.Context(), id.String())
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusOK, mcpServerView(reg))
}

func (s *Server) UpdateMCPServer(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	var req apitypes.UpdateMCPServerRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	current, err := access.GetVisible(r.Context(), id.String())
	if err != nil {
		writeMCPError(w, err)
		return
	}
	in := mcp.UpdateInput{ID: id.String(), Scope: current.Scope, AgentID: current.AgentID, Name: &current.Name, ExpectedVersion: strconv.FormatInt(req.ExpectedParentRevision, 10)}
	if req.Url != nil {
		in.URL = req.Url
	}
	if req.Transport != nil {
		v := string(*req.Transport)
		in.Transport = &v
	}
	if req.AuthType != nil {
		v := string(*req.AuthType)
		in.AuthType = &v
	}
	if req.CredentialMode != nil {
		v := string(*req.CredentialMode)
		in.CredentialMode = &v
	}
	if req.Metadata != nil {
		in.Metadata = req.Metadata
	}
	if req.Credentials != nil {
		if v, ok := (*req.Credentials)["token"].(string); ok {
			in.Token = &v
		}
		if v, ok := (*req.Credentials)["oauth_client_secret"].(string); ok {
			in.OAuthClientSecret = &v
		}
		if v, ok := (*req.Credentials)["oauth_client_id"].(string); ok {
			in.OAuthClientID = &v
		}
	}
	result, err := access.UpdateChild(r.Context(), id.String(), req.ExpectedParentRevision, in)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusOK, mcpServerView(result))
}

func (s *Server) DeleteMCPServer(w http.ResponseWriter, r *http.Request, id openapi_types.UUID, params apiserver.DeleteMCPServerParams) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	if err := access.DeleteChild(r.Context(), id.String(), params.ExpectedParentRevision); err != nil {
		writeMCPError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) ProbeMCPServer(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	visible, err := access.GetVisible(r.Context(), id.String())
	if err != nil {
		if errors.Is(err, agentaccess.ErrForbidden) || errors.Is(err, agentaccess.ErrNotFound) {
			writeMCPError(w, authz.ErrNotFound)
			return
		}
		writeMCPError(w, err)
		return
	}
	reg, err := access.Probe(r.Context(), id.String(), visible.Scope, visible.AgentID)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeData(w, http.StatusOK, mcpServerView(reg))
}

func (s *Server) StartMCPServerOAuth(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	_, authURL, flowID, expiresAt, err := access.StartOAuth(r.Context(), id.String(), s.mcpOAuthCallbackURL())
	if err != nil {
		writePluginOAuthError(w, err)
		return
	}
	writeData(w, http.StatusCreated, apitypes.MCPOAuthStart{AuthorizationUrl: authURL, FlowId: flowID, ExpiresAt: expiresAt.UTC()})
}

func (s *Server) DisconnectMCPServerOAuth(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	access, _, ok := s.beginMCPAccess(w, r)
	if !ok {
		return
	}
	visible, err := access.GetVisible(r.Context(), id.String())
	if err != nil {
		writePluginOAuthError(w, err)
		return
	}
	reg, err := access.Disconnect(r.Context(), id.String(), visible.Scope, visible.AgentID)
	if err != nil {
		writePluginOAuthError(w, err)
		return
	}
	writeData(w, http.StatusOK, mcpServerView(reg))
}

func stringPtrValue[T ~string](v *T) string {
	if v == nil {
		return ""
	}
	return string(*v)
}

func mcpServerView(reg mcp.Registration) apitypes.MCPServer {
	tools := make([]apitypes.MCPTool, len(reg.Tools))
	for i, tool := range reg.Tools {
		tools[i] = apitypes.MCPTool{
			Name:        tool.Name,
			Description: strPtr(tool.Description),
			InputSchema: mapPtr(tool.InputSchema),
			Annotations: mapPtr(tool.Annotations),
		}
	}
	status := mcpServerStatus(reg.Status)
	scope := apitypes.MCPServerScope(reg.Scope)
	transport := apitypes.MCPServerTransport(reg.Transport)
	authType := apitypes.MCPServerAuthType(reg.AuthType)
	mode := apitypes.MCPServerCredentialMode(reg.CredentialMode)
	return apitypes.MCPServer{Id: uuidPtr(reg.ID), PluginId: &reg.PluginID, ParentConfigId: uuidPtr(reg.ParentConfigID), ParentRevision: &reg.ConfigRevision, ServerKey: reg.ServerKey, Scope: &scope, Enabled: apiBoolPtr(reg.Enabled), Transport: &transport, AuthType: &authType, CredentialMode: mode, EndpointConfigured: apiBoolPtr(reg.URL != ""), BearerConfigured: apiBoolPtr(reg.CredentialRef != ""), OauthClientIdConfigured: apiBoolPtr(reg.OAuthClientID != ""), OauthClientSecretConfigured: apiBoolPtr(reg.OAuthClientSecretRef != ""), NeedsAuth: reg.Status == mcp.StatusNeedsAuth, Status: status, StatusError: strPtr(reg.StatusError), Tools: tools, Revision: &reg.ConfigRevision}
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

func readPluginBody(r *http.Request) ([]byte, error) {
	const maxBody = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		return nil, fmt.Errorf("request body too large")
	}
	return raw, nil
}

func decodeStrictJSON(r *http.Request, out any) error {
	raw, err := readPluginBody(r)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body contains multiple JSON values")
		}
		return err
	}
	return nil
}
func apiBoolPtr(v bool) *bool { return &v }
func mapPtr(v map[string]any) *map[string]any {
	if len(v) == 0 {
		return nil
	}
	return &v
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func uuidPtr(v string) *openapi_types.UUID {
	id, err := uuid.Parse(v)
	if err != nil {
		return nil
	}
	return &id
}
