package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

type staticTool struct{ name string }

func (t staticTool) Definition() pkgtools.Definition {
	return pkgtools.Definition{Name: t.name, Description: t.name}
}

func (t staticTool) Execute(context.Context, map[string]any) (string, error) { return "", nil }
func (t staticTool) ExecuteContent(context.Context, map[string]any) ([]ai.ContentBlock, error) {
	return nil, nil
}

type recordingMCPToolProvider struct {
	userID  string
	agentID string
}

func (p *recordingMCPToolProvider) ToolsForContext(_ context.Context, userID, agentID string) []pkgtools.Tool {
	p.userID = userID
	p.agentID = agentID
	return nil
}

func TestBuildToolRegistryAppliesToolOverrides(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	reg, _, _, err := buildToolRegistry(ctx, runnerConfig{
		Sandbox: sandbox.Config{Paths: sandbox.Paths{
			StellaHome: home,
			AgentRoot:  filepath.Join(home, "agents", "agent-1"),
			UserRoot:   filepath.Join(home, "users", "user-1"),
		}},
		BuiltinParams: RunnerParams{UserID: "user-1", AgentID: "agent-1"},
		BuiltinTools:  []BuiltinTool{{Tool: staticTool{name: "memory"}}},
		ToolOverrideFetcher: func(context.Context, string, string) ([]ToolOverride, error) {
			return []ToolOverride{{ToolName: "memory", Scope: ToolOverrideScopeUserAgent, Enabled: false}}, nil
		},
	}, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	if reg.Has("memory") {
		t.Fatal("memory tool is registered, want filtered by override")
	}
	if !reg.Has("bash") || !reg.Has("read") || !reg.Has("write") || !reg.Has("edit") {
		t.Fatal("core tools should remain registered")
	}
}

func TestBuildToolRegistryKeepsDelegateInternalOnly(t *testing.T) {
	home := t.TempDir()
	reg, _, delegateTool, err := buildToolRegistry(context.Background(), runnerConfig{
		Sandbox: sandbox.Config{Paths: sandbox.Paths{
			StellaHome: home,
			AgentRoot:  filepath.Join(home, "agents", "agent-1"),
			UserRoot:   filepath.Join(home, "users", "user-1"),
		}},
		BuiltinParams: RunnerParams{UserID: "user-1", AgentID: "agent-1"},
		BuiltinTools:  []BuiltinTool{{Tool: staticTool{name: "session"}}},
	}, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	if delegateTool == nil {
		t.Fatal("internal delegate adapter is unavailable to session.create/send")
	}
	if reg.Has("delegate") {
		t.Fatal("delegate is still registered on the model-facing tool surface")
	}
	if !reg.Has("session") {
		t.Fatal("session replacement is absent from the model-facing tool surface")
	}
}

func TestBuildToolRegistryDoesNotResolvePrivateToolConfigurationForGroup(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	mcpProvider := &recordingMCPToolProvider{}
	var overrideUserID, overrideAgentID string

	_, _, _, err := buildToolRegistry(ctx, runnerConfig{
		Sandbox: sandbox.Config{Paths: sandbox.Paths{
			StellaHome: home,
			AgentRoot:  filepath.Join(home, "agents", "agent-1"),
			UserRoot:   filepath.Join(home, "users", "group-1"),
		}},
		BuiltinParams:   RunnerParams{UserID: "colliding-private-user-id", GroupID: "colliding-private-user-id", AgentID: "agent-1"},
		MCPToolProvider: mcpProvider,
		ToolOverrideFetcher: func(_ context.Context, userID, agentID string) ([]ToolOverride, error) {
			overrideUserID = userID
			overrideAgentID = agentID
			return nil, nil
		},
	}, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}
	if mcpProvider.userID != "" || overrideUserID != "" {
		t.Fatalf("group tool resolution received private user IDs: MCP=%q overrides=%q", mcpProvider.userID, overrideUserID)
	}
	if mcpProvider.agentID != "agent-1" || overrideAgentID != "agent-1" {
		t.Fatalf("group tool resolution lost agent scope: MCP=%q overrides=%q", mcpProvider.agentID, overrideAgentID)
	}
}
