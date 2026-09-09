package mcp

import (
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestMCPFileServerIDRoundTrip(t *testing.T) {
	key := plugin.ResourceKey{Scope: plugin.ScopeUserAgent, UserID: "user-1", AgentID: "agent-1", Kind: plugin.ResourcePlugin, Name: "package"}
	id := mcpFileServerID(key, "remote/server")
	if id == "" {
		t.Fatal("empty file server id")
	}
	gotKey, gotServer, err := parseMCPFileServerID(id)
	if err != nil {
		t.Fatalf("parse id: %v", err)
	}
	if gotKey != key || gotServer != "remote/server" {
		t.Fatalf("round trip = %#v, %q", gotKey, gotServer)
	}
	if _, _, err := parseMCPFileServerID(id + "x"); err == nil {
		t.Fatal("tampered id accepted")
	}
}

func TestMCPFileServerIDRejectsNonCanonicalPayload(t *testing.T) {
	key := plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "server"}
	id := mcpFileServerID(key, "server")
	if _, _, err := parseMCPFileServerID(id); err != nil {
		t.Fatalf("parse standalone id: %v", err)
	}
	if _, _, err := parseMCPFileServerID("file:" + id); err == nil {
		t.Fatal("wrong prefix accepted")
	}
}
