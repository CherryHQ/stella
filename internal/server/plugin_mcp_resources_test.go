package server

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestMCPFileViewUsesCompositeAddressAndResourceMetadata(t *testing.T) {
	key := plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "remote"}
	declaration := mcpconfig.Declaration{URL: "https://example.test/mcp", Transport: mcp.TransportStreamableHTTP, Authentication: mcpconfig.Authentication{Type: mcp.AuthTypeNone, Mode: mcp.CredentialModePerUser}}
	resource := plugin.FileResource{Key: key, Digest: "sha256:content", SettingsDigest: "sha256:settings", MCP: map[string]mcpconfig.Declaration{"remote": declaration}}
	authority, err := authz.NewUserAuthority(authz.UserID("00000000-0000-0000-0000-000000000001"), true)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := mcp.RegistrationFromFileResource(resource, "remote", authority)
	if err != nil {
		t.Fatal(err)
	}
	view := mcpFileView(mcp.FileServer{ID: "mcp-file:test", Registration: reg, Resource: resource, ServerKey: "remote"})
	if view.Id == nil || *view.Id != "mcp-file:test" || view.ResourceId == nil || *view.ResourceId != key.ID() {
		t.Fatalf("address = %#v resource_id=%v", view.Id, view.ResourceId)
	}
	if view.ContentDigest == nil || *view.ContentDigest != resource.Digest || view.SettingsDigest == nil || *view.SettingsDigest != resource.SettingsDigest {
		t.Fatalf("digests = %v/%v", view.ContentDigest, view.SettingsDigest)
	}
	if view.IsStandalone == nil || !*view.IsStandalone || view.IsReadOnly == nil || *view.IsReadOnly {
		t.Fatalf("source flags = standalone %v readonly %v", view.IsStandalone, view.IsReadOnly)
	}
}
