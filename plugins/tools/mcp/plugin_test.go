package mcp

import (
	"context"
	"log/slog"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestPromptInventoryRegistrationReturnsMCPTools(t *testing.T) {
	host := &testServiceHost{lookup: testRuntimeLookup{handle: testRuntimeHandle{runtime: runtimeWrapper{manager: NewManager()}}}}
	rt, ok := LookupRuntime(host)
	if !ok || rt.Manager() == nil {
		t.Fatal("expected runtime")
	}
	rt.Manager().RegisterTool("docs", "search", "Search", "Search docs", nil, nil, nil)
	plugin, ok := pkgplugins.Get(PluginID)
	if !ok {
		t.Fatal("expected mcp plugin registration")
	}
	registry := &testRegistry{}
	plugin.Register(testHost{registry: registry, services: host})
	items, err := registry.prompt.LegacyGetTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Metadata["server_name"] != "docs" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

type testHost struct {
	registry *testRegistry
	services *testServiceHost
}

func (h testHost) Registry() pkgplugins.RegistryHost  { return h.registry }
func (h testHost) Services() pkgplugins.ServiceHost   { return h.services }
func (h testHost) SetInfo(info pkgplugins.PluginInfo) { h.registry.RegisterMetadata(info) }
func (h testHost) AddAdmin(reg pkgplugins.AdminSpec) {
	h.registry.RegisterConfig(reg)
	if reg.Status != nil || reg.Get != nil {
		h.registry.RegisterStatus(reg)
	}
}
func (h testHost) AddTool(reg pkgplugins.ToolSpec)         { h.registry.RegisterTool(reg) }
func (h testHost) AddProvider(reg pkgplugins.ProviderSpec) { h.registry.RegisterProvider(reg) }
func (h testHost) AddChannel(reg pkgplugins.ChannelSpec)   { h.registry.RegisterChannel(reg) }
func (h testHost) AddHook(reg pkgplugins.HookSpec)         { h.registry.RegisterHook(reg) }
func (h testHost) AddMemory(reg pkgplugins.MemorySpec)     { h.registry.RegisterMemory(reg) }
func (h testHost) AddRuntime(reg pkgplugins.RuntimeSpec)   { h.registry.RegisterRuntime(reg) }
func (h testHost) AddPromptInventory(reg pkgplugins.PromptInventorySpec) {
	h.registry.RegisterPromptInventory(reg)
}
func (h testHost) AddSystemPrompt(reg pkgplugins.SystemPromptSpec) {
	h.registry.RegisterSystemPrompt(reg)
}
func (h testHost) AddBeforeRun(reg pkgplugins.BeforeRunSpec) { h.registry.RegisterBeforeRun(reg) }
func (h testHost) AddBeforeToolCall(reg pkgplugins.BeforeToolCallSpec) {
	h.registry.RegisterBeforeToolCall(reg)
}
func (h testHost) AddAfterToolResult(reg pkgplugins.AfterToolResultSpec) {
	h.registry.RegisterAfterToolResult(reg)
}

type testRegistry struct {
	prompt pkgplugins.PromptInventorySpec
}

func (*testRegistry) RegisterTool(pkgplugins.ToolSpec)                 {}
func (*testRegistry) RegisterProvider(pkgplugins.ProviderSpec)         {}
func (*testRegistry) RegisterChannel(pkgplugins.ChannelSpec)           {}
func (*testRegistry) RegisterHook(pkgplugins.HookSpec)                 {}
func (*testRegistry) RegisterMemory(pkgplugins.MemorySpec)             {}
func (*testRegistry) RegisterRuntime(pkgplugins.RuntimeSpec)           {}
func (*testRegistry) RegisterConfig(pkgplugins.AdminSpec)              {}
func (*testRegistry) RegisterStatus(pkgplugins.AdminSpec)              {}
func (*testRegistry) RegisterSystemPrompt(pkgplugins.SystemPromptSpec) {}
func (*testRegistry) RegisterBeforeRun(pkgplugins.BeforeRunSpec)       {}
func (*testRegistry) RegisterBeforeToolCall(pkgplugins.BeforeToolCallSpec) {
}
func (*testRegistry) RegisterAfterToolResult(pkgplugins.AfterToolResultSpec) {}
func (r *testRegistry) RegisterPromptInventory(reg pkgplugins.PromptInventorySpec) {
	r.prompt = reg
}
func (*testRegistry) RegisterMetadata(pkgplugins.PluginInfo) {}

type testServiceHost struct{ lookup testRuntimeLookup }

func (*testServiceHost) Logger(string) *slog.Logger                 { return nil }
func (*testServiceHost) Config() pkgplugins.ConfigService           { return nil }
func (h *testServiceHost) Runtime() pkgplugins.RuntimeLookup        { return h.lookup }
func (*testServiceHost) Notifications() pkgplugins.Notifier         { return nil }
func (*testServiceHost) Scheduler() pkgplugins.SchedulerService     { return nil }
func (*testServiceHost) StateStore() pkgplugins.PluginStateStore    { return nil }
func (*testServiceHost) Auth() pkgplugins.Auth                      { return nil }
func (*testServiceHost) ChannelRuntime() pkgplugins.ChannelPlatform { return nil }
func (*testServiceHost) ReflectRuntime() pkgplugins.ReflectPlatform { return nil }

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
