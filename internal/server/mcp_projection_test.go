package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestAgentMCPServerResponseOmitsPrivateRegistrationFields(t *testing.T) {
	response := agentMCPServerResponse(mcp.FileServer{
		ID:        "mcp-file:test",
		ServerKey: "main",
		Resource: plugin.FileResource{
			Key:    plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "github"},
			Digest: "sha256:resource",
			MCP:    map[string]mcpconfig.Declaration{"main": {URL: "https://secret.example.test/mcp", Transport: "sse", Authentication: mcpconfig.Authentication{Type: "bearer", Mode: "shared"}}},
		},
		Registration: mcp.Registration{
			ID: "auth-id", Scope: "system", Name: "github", AuthType: "bearer", CredentialMode: "shared",
			Status: mcp.StatusOK, Tools: []mcp.CatalogTool{{Name: "search"}},
		},
	})
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, private := range []string{"secret.example.test", `"url"`, `"payload"`, `"metadata"`, `"credential_ref"`, `"credential_refs"`, `"config_id"`, `"parent_config_id"`, `"plugin_id"`} {
		if strings.Contains(body, private) {
			t.Fatalf("safe effective response contains %q: %s", private, body)
		}
	}
	if !response.Readable || response.Name == nil || *response.Name != "github" || response.Id == nil || *response.Id != "mcp-file:test" {
		t.Fatalf("unexpected identity/readability projection: %+v", response)
	}
}
