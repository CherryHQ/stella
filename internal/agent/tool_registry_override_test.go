package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

type staticTool struct{ name string }

func (t staticTool) Definition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        t.name,
		Description: t.name,
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}
}

func (t staticTool) Execute(context.Context, map[string]any) (string, error) {
	return "executed:" + t.name, nil
}

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

func TestBuildToolRegistryAssemblesAuthorizedBuiltinsWithValidSchemas(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	reg, _, err := buildToolRegistry(ctx, runnerConfig{
		Sandbox: sandbox.Config{Paths: sandbox.Paths{
			StellaHome: home,
			AgentRoot:  filepath.Join(home, "agents", "agent-1"),
			UserRoot:   filepath.Join(home, "users", "user-1"),
		}},
		BuiltinParams: RunnerParams{UserID: "user-1", AgentID: "agent-1"},
		BuiltinTools: []BuiltinTool{
			{Tool: staticTool{name: "authorized"}, Available: BuiltinToolAvailable},
			{Tool: staticTool{name: "unavailable"}, Available: func(context.Context, RunnerParams) bool { return false }},
			{Tool: staticTool{name: "overridden"}, Available: BuiltinToolAvailable},
		},
		ToolOverrideFetcher: func(context.Context, string, string) ([]ToolOverride, error) {
			return []ToolOverride{{ToolName: "overridden", Scope: ToolOverrideScopeUserAgent, Enabled: false}}, nil
		},
	}, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if err != nil {
		t.Fatalf("buildToolRegistry: %v", err)
	}

	for _, name := range []string{"bash", "read", "write", "edit", "authorized", "delegate"} {
		if !reg.Has(name) {
			t.Errorf("authorized runtime registry is missing %q", name)
		}
	}
	for _, name := range []string{"unavailable", "overridden"} {
		if reg.Has(name) {
			t.Errorf("runtime registry exposed unavailable tool %q", name)
		}
	}

	// Every definition passed to a model must be an object-shaped JSON schema;
	// this catches tools that are discoverable but unusable at invocation time.
	for _, def := range reg.Definitions() {
		if def.Name == "" || def.Description == "" {
			t.Errorf("invalid tool definition identity: %+v", def)
		}
		if got := def.InputSchema["type"]; got != "object" {
			t.Errorf("tool %q schema type = %v, want object", def.Name, got)
		}
		if _, err := json.Marshal(def.InputSchema); err != nil {
			t.Errorf("tool %q schema is not JSON encodable: %v", def.Name, err)
		}
	}

	got, err := reg.Execute(ctx, "authorized", map[string]any{})
	if err != nil {
		t.Fatalf("execute authorized built-in: %v", err)
	}
	if got != "executed:authorized" {
		t.Fatalf("authorized built-in result = %q", got)
	}
}
