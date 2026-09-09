package mcp

import (
	"context"
	"errors"
	"net/url"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestToolsForFileSessionProjectsResourcesAndDisabledTools(t *testing.T) {
	authority := fileSessionTestAuthority(t, "file-provider-user")
	resource := plugin.FileResource{
		Key:           plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "provider"},
		MCP:           map[string]mcpconfig.Declaration{"main/server": {URL: "https://mcp.example.test", Transport: TransportStreamableHTTP}},
		DisabledTools: []string{url.PathEscape("main/server") + "/" + url.PathEscape("echo")},
	}
	service := &Service{}
	session := NewFileSession(service)
	defer func() { _ = session.Close() }()
	var connects atomic.Int32
	session.SetConnectForTesting(func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error) {
		connects.Add(1)
		return &fileSessionFakeClient{}, nil
	})
	snapshot, err := NewToolProvider(service).ToolsForFileSession(t.Context(), session, []plugin.FileResource{resource}, authority)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tools) != 0 || len(snapshot.Directory) != 1 {
		t.Fatalf("snapshot = tools %d, directory %#v", len(snapshot.Tools), snapshot.Directory)
	}
	entry := snapshot.Directory[0]
	if entry.PluginID != resource.Key.ID() || !entry.Ready || entry.Status != StatusOK || len(entry.Tools) != 0 {
		t.Fatalf("directory entry = %#v", entry)
	}
	if len(snapshot.SuccessfulPluginIDs) != 1 || snapshot.SuccessfulPluginIDs[0] != resource.Key.ID() {
		t.Fatalf("successful resources = %v", snapshot.SuccessfulPluginIDs)
	}
	if connects.Load() != 1 {
		t.Fatalf("connects = %d, want one shared connection", connects.Load())
	}
}

func TestToolsForFileSessionKeepsIndependentFailuresAndFailsClosedPolicy(t *testing.T) {
	authority := fileSessionTestAuthority(t, "file-provider-user-2")
	good := plugin.FileResource{
		Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "good"},
		MCP: map[string]mcpconfig.Declaration{"main": {URL: "https://mcp.example.test", Transport: TransportStreamableHTTP}},
	}
	bad := plugin.FileResource{
		Key:           plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "bad"},
		MCP:           map[string]mcpconfig.Declaration{"main": {URL: "https://mcp.example.test", Transport: TransportStreamableHTTP}},
		DisabledTools: []string{"main/%ZZ"},
	}
	service := &Service{}
	session := NewFileSession(service)
	defer func() { _ = session.Close() }()
	var connects atomic.Int32
	session.SetConnectForTesting(func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error) {
		connects.Add(1)
		return &fileSessionFakeClient{}, nil
	})
	snapshot, err := NewToolProvider(service).ToolsForFileSession(t.Context(), session, []plugin.FileResource{bad, good}, authority)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Directory) != 2 || len(snapshot.Tools) != 1 {
		t.Fatalf("snapshot = tools %d, directory %#v", len(snapshot.Tools), snapshot.Directory)
	}
	var badReady, goodReady bool
	var badReason string
	for _, entry := range snapshot.Directory {
		if entry.PluginID == bad.Key.ID() {
			badReady, badReason = entry.Ready, entry.StatusError
		}
		if entry.PluginID == good.Key.ID() {
			goodReady = entry.Ready
		}
	}
	if badReady || badReason == "" || !goodReady {
		t.Fatalf("independent directory results = %#v", snapshot.Directory)
	}
	if connects.Load() != 1 {
		t.Fatalf("invalid policy opened %d connections, want one", connects.Load())
	}
	if _, err := snapshot.Tools[0].Execute(t.Context(), nil); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("file proxy without turn authority = %v, want unauthenticated", err)
	}
}

type fileSessionEmptyClient struct{}

func (fileSessionEmptyClient) ListTools(context.Context) ([]*mcpsdk.Tool, error) { return nil, nil }
func (fileSessionEmptyClient) CallTool(context.Context, string, map[string]any) (*mcpsdk.CallToolResult, error) {
	return nil, errors.New("unused")
}
func (fileSessionEmptyClient) Close() error { return nil }

func TestToolsForFileSessionDistinguishesEmptyReadyFromFailedServer(t *testing.T) {
	authority := fileSessionTestAuthority(t, "file-provider-user-3")
	resources := []plugin.FileResource{
		{Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "empty"}, MCP: map[string]mcpconfig.Declaration{"main": {URL: "https://mcp.example.test", Transport: TransportStreamableHTTP}}},
		{Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "broken"}, MCP: map[string]mcpconfig.Declaration{"main": {URL: "https://mcp.example.test", Transport: TransportStreamableHTTP}}},
	}
	service := &Service{}
	session := NewFileSession(service)
	defer func() { _ = session.Close() }()
	session.SetConnectForTesting(func(_ context.Context, reg Registration, _ CredentialOwner, _ func()) (RemoteClient, error) {
		if reg.Name == "broken" {
			return nil, errors.New("offline")
		}
		return fileSessionEmptyClient{}, nil
	})
	snapshot, err := NewToolProvider(service).ToolsForFileSession(t.Context(), session, resources, authority)
	if err != nil || len(snapshot.Tools) != 0 || len(snapshot.Directory) != 2 {
		t.Fatalf("snapshot = %#v, err = %v", snapshot, err)
	}
	var empty, broken *pkgplugins.MCPDirectoryEntry
	for i := range snapshot.Directory {
		entry := &snapshot.Directory[i]
		switch entry.PluginID {
		case resources[0].Key.ID():
			empty = entry
		case resources[1].Key.ID():
			broken = entry
		}
	}
	if empty == nil || !empty.Ready || empty.Status != StatusOK || broken == nil || broken.Ready || broken.StatusError == "" {
		t.Fatalf("directory = %#v", snapshot.Directory)
	}
	if len(snapshot.SuccessfulPluginIDs) != 1 || snapshot.SuccessfulPluginIDs[0] != resources[0].Key.ID() {
		t.Fatalf("successful IDs = %v", snapshot.SuccessfulPluginIDs)
	}
}

func TestToolsForFileSessionKeepsPluginAndMCPSameNameScoped(t *testing.T) {
	authority := fileSessionTestAuthority(t, "file-provider-user-4")
	declaration := mcpconfig.Declaration{URL: "https://mcp.example.test", Transport: TransportStreamableHTTP}
	resources := []plugin.FileResource{
		{Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourcePlugin, Name: "same"}, Package: &agentpackage.Package{}, MCP: map[string]mcpconfig.Declaration{"main": declaration}},
		{Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "same"}, MCP: map[string]mcpconfig.Declaration{"main": declaration}},
	}
	service := &Service{}
	session := NewFileSession(service)
	defer func() { _ = session.Close() }()
	session.SetConnectForTesting(func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error) {
		return &fileSessionFakeClient{}, nil
	})
	snapshot, err := NewToolProvider(service).ToolsForFileSession(t.Context(), session, resources, authority)
	if err != nil || len(snapshot.Tools) != 2 || len(snapshot.Directory) != 2 {
		t.Fatalf("same-name scoped snapshot = tools %d, directory %d, err %v", len(snapshot.Tools), len(snapshot.Directory), err)
	}
	if snapshot.Directory[0].PluginID == snapshot.Directory[1].PluginID || snapshot.Tools[0].Definition().Name == snapshot.Tools[1].Definition().Name {
		t.Fatalf("same-name scoped identities collided: %#v / %#v", snapshot.Directory, snapshot.Tools)
	}
}
