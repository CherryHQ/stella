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
	items, err := registry.prompt.GetTools(context.Background())
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

func (h testHost) Registry() pkgplugins.RegistryHost { return h.registry }
func (h testHost) Services() pkgplugins.ServiceHost  { return h.services }

type testRegistry struct {
	prompt pkgplugins.PromptInventoryRegistration
}

func (*testRegistry) RegisterTool(pkgplugins.ToolRegistration)                 {}
func (*testRegistry) RegisterProvider(pkgplugins.ProviderRegistration)         {}
func (*testRegistry) RegisterChannel(pkgplugins.ChannelRegistration)           {}
func (*testRegistry) RegisterHook(pkgplugins.HookRegistration)                 {}
func (*testRegistry) RegisterMemory(pkgplugins.MemoryRegistration)             {}
func (*testRegistry) RegisterRuntime(pkgplugins.RuntimeRegistration)           {}
func (*testRegistry) RegisterConfig(pkgplugins.ConfigRegistration)             {}
func (*testRegistry) RegisterStatus(pkgplugins.StatusRegistration)             {}
func (*testRegistry) RegisterSystemPrompt(pkgplugins.SystemPromptRegistration) {}
func (*testRegistry) RegisterBeforeRun(pkgplugins.BeforeRunRegistration)       {}
func (r *testRegistry) RegisterPromptInventory(reg pkgplugins.PromptInventoryRegistration) {
	r.prompt = reg
}
func (*testRegistry) RegisterMetadata(pkgplugins.PluginMeta) {}

type testServiceHost struct{ lookup testRuntimeLookup }

func (*testServiceHost) Logger(string) *slog.Logger                        { return nil }
func (*testServiceHost) Config() pkgplugins.ConfigService                  { return nil }
func (h *testServiceHost) Runtime() pkgplugins.RuntimeLookup               { return h.lookup }
func (*testServiceHost) Notifications() pkgplugins.NotificationService     { return nil }
func (*testServiceHost) ChannelRuntime() pkgplugins.ChannelRuntimeServices { return nil }
func (*testServiceHost) ReflectRuntime() pkgplugins.ReflectRuntimeServices { return nil }

type testRuntimeLookup struct{ handle testRuntimeHandle }

func (l testRuntimeLookup) Get(pluginID, runtimeName string) (pkgplugins.RuntimeHandle, bool) {
	return l.handle, pluginID == PluginID && runtimeName == RuntimeName
}

type testRuntimeHandle struct{ runtime runtimeAccessor }

func (h testRuntimeHandle) Snapshot(context.Context) (pkgplugins.RuntimeSnapshot, error) {
	return pkgplugins.RuntimeSnapshot{}, nil
}
func (h testRuntimeHandle) RuntimeAccessor() any { return h.runtime }
