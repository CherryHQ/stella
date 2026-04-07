package mcp

import "testing"

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
