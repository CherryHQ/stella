package mcp

import (
	"context"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestRegisterPluginUsesSharedManager(t *testing.T) {
	manager := NewManager()
	manager.AddTool("docs", "search", "Search", "Search docs", nil, nil, nil)
	host := &testHost{}
	RegisterPlugin(host, manager)

	rt, ok := LookupRuntime(testRuntimeLookup{handle: testRuntimeHandle{runtime: runtimeWrapper{manager: manager}}})
	if !ok || rt.Manager() == nil {
		t.Fatal("expected runtime")
	}
	if got := len(rt.Manager().ValidTools()); got != 1 {
		t.Fatalf("expected 1 valid tool, got %d", got)
	}
}

func TestValidToolsReturnsPromptInfo(t *testing.T) {
	manager := NewManager()
	manager.AddTool("docs", "search", "Search", "Search docs", nil, nil, nil)
	tools := manager.ValidTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].ServerName != "docs" {
		t.Fatalf("expected server_name=docs, got %q", tools[0].ServerName)
	}
}

type testHost struct{}

func (*testHost) SetInfo(pkgplugins.PluginInfo)                     {}
func (*testHost) AddAdmin(pkgplugins.AdminSpec)                     {}
func (*testHost) AddTool(pkgplugins.ToolSpec)                       {}
func (*testHost) AddProvider(pkgplugins.ProviderSpec)               {}
func (*testHost) AddChannel(pkgplugins.ChannelSpec)                 {}
func (*testHost) AddHook(pkgplugins.HookSpec)                       {}
func (*testHost) AddMemory(pkgplugins.MemorySpec)                   {}
func (*testHost) AddRuntime(pkgplugins.RuntimeSpec)                 {}
func (*testHost) AddPromptInventory(pkgplugins.PromptInventorySpec) {}
func (*testHost) AddSystemPrompt(pkgplugins.SystemPromptSpec)       {}
func (*testHost) AddBeforeRun(pkgplugins.BeforeRunSpec)             {}
func (*testHost) AddBeforeToolCall(pkgplugins.BeforeToolCallSpec)   {}
func (*testHost) AddAfterToolResult(pkgplugins.AfterToolResultSpec) {}
func (*testHost) AddSessionEnv(pkgplugins.SessionEnvSpec)           {}
func (*testHost) AddBundledSkill(pkgplugins.BundledSkillSpec)       {}

type testRuntimeLookup struct{ handle testRuntimeHandle }

func (l testRuntimeLookup) Get(pluginID, runtimeName string) (pkgplugins.RuntimeHandle, bool) {
	return l.handle, pluginID == PluginID && runtimeName == RuntimeName
}

func (l testRuntimeLookup) Lookup(pluginID, runtimeName string) (pkgplugins.RuntimeHandle, bool) {
	return l.Get(pluginID, runtimeName)
}

type testRuntimeHandle struct{ runtime runtimeAccessor }

func (h testRuntimeHandle) Snapshot(context.Context) (pkgplugins.RuntimeStatus, error) {
	return pkgplugins.RuntimeStatus{}, nil
}

func (h testRuntimeHandle) Status(ctx context.Context) (pkgplugins.RuntimeStatus, error) {
	return h.Snapshot(ctx)
}
func (h testRuntimeHandle) RuntimeAccessor() any { return h.runtime }
