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

func TestBuildToolRegistryAppliesToolOverrides(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	reg, _, err := buildToolRegistry(ctx, runnerConfig{
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
