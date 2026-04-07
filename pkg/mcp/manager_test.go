package mcp

import (
	"testing"
)

func TestManagerRegistersValidTools(t *testing.T) {
	mgr := NewManager()
	mgr.Configure(Config{Servers: []ServerConfig{{Name: "github", Enabled: true, Transport: TransportStdio, Command: "npx"}}}, true)
	tool := mgr.RegisterTool("github", "search_repos", "search_repos", "Search repos", map[string]any{"type": "object"}, nil, nil)
	if !mgr.Enabled() {
		t.Fatal("expected manager enabled")
	}
	valid := mgr.ValidTools()
	if len(valid) != 1 {
		t.Fatalf("len(valid) = %d, want 1", len(valid))
	}
	tool = valid[0]
	if tool.ID == "" {
		t.Fatal("expected tool ID")
	}
	resolved, ok := mgr.Resolve(tool.ID)
	if !ok {
		t.Fatal("expected resolve success")
	}
	if resolved.ServerName != "github" || resolved.ToolName != "search_repos" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestManagerCanonicalIDsAreDeterministicAcrossRebuilds(t *testing.T) {
	mgr := NewManager()
	mgr.serverTools = map[string][]ToolInfo{
		"b-server": {
			{ServerName: "b-server", ToolName: "tool!", Name: "tool!"},
		},
		"a-server": {
			{ServerName: "a-server", ToolName: "tool", Name: "tool"},
		},
	}

	mgr.rebuildToolsLocked()
	first := mgr.ListTools()
	firstIDs := map[string]string{}
	for _, tool := range first {
		firstIDs[tool.ServerName+"/"+tool.ToolName] = tool.ID
	}

	mgr.serverTools = map[string][]ToolInfo{
		"a-server": {
			{ServerName: "a-server", ToolName: "tool", Name: "tool"},
		},
		"b-server": {
			{ServerName: "b-server", ToolName: "tool!", Name: "tool!"},
		},
	}
	mgr.rebuildToolsLocked()
	second := mgr.ListTools()
	for _, tool := range second {
		key := tool.ServerName + "/" + tool.ToolName
		if firstIDs[key] != tool.ID {
			t.Fatalf("canonical ID for %s changed: %q -> %q", key, firstIDs[key], tool.ID)
		}
	}
}
