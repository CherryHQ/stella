package pluginhost

import (
	"context"
	"errors"
	"testing"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
	pluginmemory "github.com/vaayne/anna/plugins/memory"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

func TestRegisterLegacyCapabilitiesBuildsToolThroughHost(t *testing.T) {
	const name = "host-legacy-tool"
	plugintools.Register(name, plugintools.Registration{
		Factory: func(bc plugintools.BuildContext) (tools.Tool, error) {
			if bc.WorkDir != "/tmp/work" {
				t.Fatalf("unexpected work dir: %q", bc.WorkDir)
			}
			return testTool{name: name}, nil
		},
	})

	host := New(&stubStore{plugins: map[string]config.Plugin{
		"tool/" + name: {ID: "tool/" + name, Enabled: true},
	}})
	host.RegisterLegacyCapabilities(LegacyBuildDeps{})

	built := host.BuildEnabledTools(context.Background(), plugintools.BuildContext{WorkDir: "/tmp/work"})
	for _, item := range built {
		if tool, ok := item.(testTool); ok && tool.name == name {
			return
		}
	}
	t.Fatalf("expected %q tool in %#v", name, built)
}

func TestRegisterLegacyCapabilitiesBuildsHookThroughHost(t *testing.T) {
	const name = "host-legacy-hook"
	pluginhooks.Register(name, pluginhooks.Registration{
		Factory: func(bc pluginhooks.BuildContext) (hooks.HookPlugin, error) {
			if bc.ToolsBinDir != "/tmp/bin" {
				t.Fatalf("unexpected tools bin dir: %q", bc.ToolsBinDir)
			}
			return testHook{name: name}, nil
		},
	})

	host := New(&stubStore{plugins: map[string]config.Plugin{
		"hook/" + name: {ID: "hook/" + name, Enabled: true},
	}})
	host.RegisterLegacyCapabilities(LegacyBuildDeps{})

	built := host.BuildEnabledHooks(context.Background(), pluginhooks.BuildContext{ToolsBinDir: "/tmp/bin"})
	for _, item := range built {
		if hook, ok := item.(testHook); ok && hook.name == name {
			return
		}
	}
	t.Fatalf("expected %q hook in %#v", name, built)
}

func TestRegisterLegacyCapabilitiesBuildsProviderThroughHost(t *testing.T) {
	const name = "host-legacy-provider"
	pluginproviders.Register(name, pluginproviders.Registration{
		Meta: pluginproviders.ProviderMeta{Name: "Host Legacy Provider", DefaultURL: "https://example.test"},
		Factory: func(cfg pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
			return testProvider{apiKey: cfg.APIKey, baseURL: cfg.BaseURL}, nil
		},
	})

	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterLegacyCapabilities(LegacyBuildDeps{})

	adapter, err := host.BuildProvider(name, map[string]any{"api_key": "k", "base_url": "https://provider.test"})
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := adapter.(testProvider)
	if !ok {
		t.Fatalf("unexpected provider type %T", adapter)
	}
	if provider.apiKey != "k" || provider.baseURL != "https://provider.test" {
		t.Fatalf("unexpected provider config: %#v", provider)
	}
}

func TestRegisterLegacyCapabilitiesBuildsMemoryThroughHost(t *testing.T) {
	const name = "host-legacy-memory"
	pluginmemory.Register(name, pluginmemory.Registration{
		Factory: func(_ context.Context, bc pluginmemory.BuildContext) (memory.Provider, error) {
			if bc.AnnaHome != "/tmp/anna" {
				t.Fatalf("unexpected anna home: %q", bc.AnnaHome)
			}
			if bc.Config["enabled"] != true {
				t.Fatalf("unexpected config: %#v", bc.Config)
			}
			if bc.SummarizerFn == nil {
				t.Fatal("expected summarizer")
			}
			return testMemory{name: name}, nil
		},
	})

	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterLegacyCapabilities(LegacyBuildDeps{})

	provider, err := host.BuildMemory(context.Background(), name, nil, "/tmp/anna", map[string]any{"enabled": true}, func(context.Context, string) (string, error) {
		return "summary", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mem, ok := provider.(testMemory)
	if !ok {
		t.Fatalf("unexpected memory provider type %T", provider)
	}
	if mem.name != name {
		t.Fatalf("unexpected memory provider: %#v", mem)
	}
}

func TestBuildProviderRequiresHostRegistration(t *testing.T) {
	const name = "host-unprimed-provider"
	pluginproviders.Register(name, pluginproviders.Registration{
		Meta: pluginproviders.ProviderMeta{Name: "Unprimed Provider"},
		Factory: func(cfg pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
			return testProvider{apiKey: cfg.APIKey}, nil
		},
	})

	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	_, err := host.BuildProvider(name, map[string]any{"api_key": "k"})
	if !errors.Is(err, providers.ErrProviderNotFound) {
		t.Fatalf("BuildProvider error = %v, want %v", err, providers.ErrProviderNotFound)
	}
}

func TestBuildMemoryRequiresHostRegistration(t *testing.T) {
	const name = "host-unprimed-memory"
	pluginmemory.Register(name, pluginmemory.Registration{
		Factory: func(_ context.Context, _ pluginmemory.BuildContext) (memory.Provider, error) {
			return testMemory{name: name}, nil
		},
	})

	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	provider, err := host.BuildMemory(context.Background(), name, nil, "/tmp/anna", map[string]any{"enabled": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider != nil {
		t.Fatalf("expected nil provider without host registration, got %T", provider)
	}
}

type testTool struct{ name string }

func (t testTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (testTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

type testHook struct{ name string }

func (h testHook) Name() string { return h.name }
func (testHook) Priority() int  { return 0 }

type testProvider struct {
	apiKey  string
	baseURL string
}

func (p testProvider) API() string { return "test" }
func (testProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, nil
}
func (testProvider) StreamSimple(context.Context, ai.Model, ai.Context, ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return nil, nil
}

type testMemory struct{ name string }

func (m testMemory) Name() string { return m.name }
func (testMemory) Bootstrap(context.Context, memory.Session) error {
	return nil
}
func (testMemory) Append(context.Context, memory.Session, ...ai.Message) error {
	return nil
}
func (testMemory) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, nil
}
func (testMemory) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}
func (testMemory) Close() error { return nil }
