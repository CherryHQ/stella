package server

import (
	"errors"
	"net/http"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/mcp"
)

// registryRateLimitWrites the 503 with the upstream Retry-After when present.
func writeRegistryError(w http.ResponseWriter, err error) {
	var rateLimited *mcp.RegistryRateLimitError
	if errors.As(err, &rateLimited) {
		if rateLimited.RetryAfter != "" {
			w.Header().Set("Retry-After", rateLimited.RetryAfter)
		}
		writeError(w, http.StatusServiceUnavailable, "MCP registry is rate limiting requests; try again shortly")
		return
	}
	writeError(w, http.StatusBadGateway, "MCP registry is unavailable")
}

func registryPageSize(pageSize *int) int {
	const def, max = 20, 50
	if pageSize == nil || *pageSize < 1 {
		return def
	}
	if *pageSize > max {
		return max
	}
	return *pageSize
}

func registryParam(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// ListMCPRegistryServers handles GET /api/mcp/registry/servers.
func (s *Server) ListMCPRegistryServers(w http.ResponseWriter, r *http.Request, params apiserver.ListMCPRegistryServersParams) {
	if s.mcpCatalog == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	if UserFromContext(r.Context()) == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	page, err := s.mcpCatalog.Search(
		r.Context(),
		registryParam(params.Q),
		registryPageSize(params.PageSize),
		registryParam(params.PageToken),
	)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	out := make([]apitypes.MCPRegistryServer, len(page.Servers))
	for i, srv := range page.Servers {
		out[i] = registryServerResponse(srv)
	}
	var next *string
	if page.NextPageToken != "" {
		next = &page.NextPageToken
	}
	writeData(w, http.StatusOK, apitypes.MCPRegistryServerList{Servers: out, NextPageToken: next})
}

// GetMCPRegistryServer handles GET /api/mcp/registry/servers/{source}/{id}.
func (s *Server) GetMCPRegistryServer(w http.ResponseWriter, r *http.Request, source string, id string) {
	if s.mcpCatalog == nil {
		writeCapabilityUnavailable(w, capMCP)
		return
	}
	if UserFromContext(r.Context()) == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	srv, err := s.mcpCatalog.Get(r.Context(), source, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no streamable-http remote") {
			writeError(w, http.StatusNotFound, "registry server not found")
			return
		}
		writeRegistryError(w, err)
		return
	}
	writeData(w, http.StatusOK, registryServerResponse(srv))
}

func registryServerResponse(srv mcp.CatalogServer) apitypes.MCPRegistryServer {
	out := apitypes.MCPRegistryServer{
		Source:    srv.Source,
		Id:        srv.ID,
		Name:      srv.Name,
		Url:       srv.URL,
		Transport: apitypes.MCPRegistryServerTransport(srv.Transport),
		Auth:      apitypes.MCPRegistryServerAuth(srv.Auth),
	}
	set := func(v string) *string { return &v }
	if srv.Description != "" {
		out.Description = set(srv.Description)
	}
	if srv.Version != "" {
		out.Version = set(srv.Version)
	}
	if srv.Repository != "" {
		out.Repository = set(srv.Repository)
	}
	if len(srv.Headers) > 0 {
		headers := make([]struct {
			Description *string `json:"description,omitempty"`
			IsSecret    *bool   `json:"is_secret,omitempty"`
			Name        string  `json:"name"`
			Required    *bool   `json:"required,omitempty"`
			Template    *string `json:"template,omitempty"`
		}, len(srv.Headers))
		for i, h := range srv.Headers {
			template, required, isSecret, description := h.Template, h.Required, h.Secret, h.Description
			headers[i] = struct {
				Description *string `json:"description,omitempty"`
				IsSecret    *bool   `json:"is_secret,omitempty"`
				Name        string  `json:"name"`
				Required    *bool   `json:"required,omitempty"`
				Template    *string `json:"template,omitempty"`
			}{Name: h.Name, Template: &template, Required: &required, IsSecret: &isSecret, Description: &description}
		}
		out.Headers = &headers
	}
	return out
}
