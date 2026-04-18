package mcp

import (
	"context"
	"log/slog"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestPromptInventoryRegistrationReturnsMCPTools(t *testing.T) {
	lookup := testRuntimeLookup{handle: testRuntimeHandle{runtime: runtimeWrapper{manager: NewManager()}}}
	rt, ok := LookupRuntime(lookup)
	if !ok || rt.Manager() == nil {
		t.Fatal("expected runtime")
	}
	rt.Manager().AddTool("docs", "search", "Search", "Search docs", nil, nil, nil)
	plugin, ok := pkgplugins.Get(PluginID)
	if !ok {
		t.Fatal("expected mcp plugin registration")
	}
	host := &testHost{}
	plugin.Register(host)
	items, err := host.prompt.GetTools(context.Background(), pkgplugins.PromptInventoryContext{
		Platform: testPlatform{lookup: lookup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Metadata["server_name"] != "docs" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

type testHost struct {
	prompt pkgplugins.PromptInventorySpec
}

func (*testHost) SetInfo(pkgplugins.PluginInfo)                           {}
func (*testHost) AddAdmin(pkgplugins.AdminSpec)                           {}
func (*testHost) AddTool(pkgplugins.ToolSpec)                             {}
func (*testHost) AddProvider(pkgplugins.ProviderSpec)                     {}
func (*testHost) AddChannel(pkgplugins.ChannelSpec)                       {}
func (*testHost) AddHook(pkgplugins.HookSpec)                             {}
func (*testHost) AddMemory(pkgplugins.MemorySpec)                         {}
func (*testHost) AddRuntime(pkgplugins.RuntimeSpec)                       {}
func (h *testHost) AddPromptInventory(reg pkgplugins.PromptInventorySpec) { h.prompt = reg }
func (*testHost) AddSystemPrompt(pkgplugins.SystemPromptSpec)             {}
func (*testHost) AddBeforeRun(pkgplugins.BeforeRunSpec)                   {}
func (*testHost) AddBeforeToolCall(pkgplugins.BeforeToolCallSpec)         {}
func (*testHost) AddAfterToolResult(pkgplugins.AfterToolResultSpec)       {}
func (*testHost) AddBinary(pkgplugins.BinarySpec)                         {}

type testPlatform struct{ lookup testRuntimeLookup }

func (testPlatform) Logger() *slog.Logger                        { return nil }
func (testPlatform) ConfigStore() pkgplugins.ConfigStore         { return nil }
func (testPlatform) StateStore() pkgplugins.StateStore           { return nil }
func (testPlatform) Scheduler() pkgplugins.Scheduler             { return nil }
func (testPlatform) Notifier() pkgplugins.Notifier               { return nil }
func (testPlatform) Auth() pkgplugins.Auth                       { return nil }
func (p testPlatform) RuntimeLookup() pkgplugins.RuntimeLookup   { return p.lookup }
func (testPlatform) ChannelPlatform() pkgplugins.ChannelPlatform { return nil }
func (testPlatform) ReflectPlatform() pkgplugins.ReflectPlatform { return nil }

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
