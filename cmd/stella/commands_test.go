package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/pluginhost"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/resources/binaries"
)

type commandTestProvider struct{}

func (commandTestProvider) API() string { return "anthropic" }
func (commandTestProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("not implemented")
}

func (commandTestProvider) StreamSimple(context.Context, ai.Model, ai.Context, ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("not implemented")
}

func testProviderStreamBuilder(api, apiKey, baseURL string) (providers.StreamFunc, error) {
	return providers.AdapterStreamFunc(commandTestProvider{}), nil
}

func TestIntentClassifierStreamFuncBuilderUsesProvidedProviderType(t *testing.T) {
	ph := pluginhost.New(commandTestStore{})
	ph.AddProvider(pkgplugins.ProviderSpec{
		PluginID: "provider/openai",
		Name:     "openai",
		Build: func(ctx pkgplugins.ProviderContext) (providers.ProviderAdapter, error) {
			if got := ctx.State.Config["api_key"]; got != "k" {
				t.Fatalf("api_key = %#v, want %q", got, "k")
			}
			if got := ctx.State.Config["base_url"]; got != "https://example.com" {
				t.Fatalf("base_url = %#v, want %q", got, "https://example.com")
			}
			return commandTestProvider{}, nil
		},
	})

	stream, err := intentClassifierStreamFuncBuilder(ph)(context.Background(), "openai", config.ProviderCreds{Type: "primary", APIKey: "k", BaseURL: "https://example.com"})
	if err != nil {
		t.Fatalf("intentClassifierStreamFuncBuilder: %v", err)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream func")
	}
}

type commandTestStore struct{}

func (commandTestStore) ListProviders(context.Context) ([]config.Provider, error) { return nil, nil }
func (commandTestStore) GetProvider(context.Context, string) (config.Provider, error) {
	return config.Provider{}, errors.New("not found")
}
func (commandTestStore) CreateProvider(context.Context, config.Provider) error     { return nil }
func (commandTestStore) UpdateProvider(context.Context, config.Provider) error     { return nil }
func (commandTestStore) DeleteProvider(context.Context, string) error              { return nil }
func (commandTestStore) SetProviderOrg(context.Context, string, string) error      { return nil }
func (commandTestStore) ListAgents(context.Context) ([]config.Agent, error)        { return nil, nil }
func (commandTestStore) ListEnabledAgents(context.Context) ([]config.Agent, error) { return nil, nil }
func (commandTestStore) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{}, nil
}
func (commandTestStore) CreateAgent(context.Context, config.Agent) error { return nil }
func (commandTestStore) UpdateAgent(context.Context, config.Agent) error { return nil }
func (commandTestStore) DeleteAgent(context.Context, string) error       { return nil }
func (commandTestStore) ListAccessibleAgents(context.Context, string) ([]config.Agent, error) {
	return nil, nil
}
func (commandTestStore) SetAgentOrg(context.Context, string, string) error      { return nil }
func (commandTestStore) ListChannels(context.Context) ([]config.Channel, error) { return nil, nil }
func (commandTestStore) ListChannelsByType(context.Context, string) ([]config.Channel, error) {
	return nil, nil
}

func (commandTestStore) GetChannel(context.Context, string) (config.Channel, error) {
	return config.Channel{}, nil
}
func (commandTestStore) UpsertChannel(context.Context, config.Channel) error { return nil }
func (commandTestStore) DeleteChannel(context.Context, string) error         { return nil }
func (commandTestStore) SetChannelOrg(context.Context, string, string) error { return nil }
func (commandTestStore) ListPlugins(context.Context) ([]config.Plugin, error) {
	return nil, nil
}

func (commandTestStore) ListPluginOverrides(context.Context) ([]config.Plugin, error) {
	return nil, nil
}

func (commandTestStore) ListPluginsByKind(context.Context, string) ([]config.Plugin, error) {
	return nil, nil
}
func (commandTestStore) ListEnabledPlugins(context.Context) ([]config.Plugin, error) { return nil, nil }
func (commandTestStore) GetPlugin(context.Context, string) (config.Plugin, error) {
	return config.Plugin{}, nil
}
func (commandTestStore) UpsertPlugin(context.Context, config.Plugin) error    { return nil }
func (commandTestStore) SetPluginEnabled(context.Context, string, bool) error { return nil }
func (commandTestStore) SetPluginConfig(context.Context, string, map[string]any) error {
	return nil
}
func (commandTestStore) DeletePlugin(context.Context, string) error { return nil }
func (commandTestStore) GetManifestPluginOverride(context.Context, string) (config.ManifestPluginOverride, bool, error) {
	return config.ManifestPluginOverride{}, false, nil
}

func (commandTestStore) ListManifestPluginOverrides(context.Context) ([]config.ManifestPluginOverride, error) {
	return nil, nil
}

func (commandTestStore) UpsertManifestPluginOverride(context.Context, config.ManifestPluginOverride) error {
	return nil
}
func (commandTestStore) DeleteManifestPluginOverride(context.Context, string) error { return nil }
func (commandTestStore) GetChatAgent(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (commandTestStore) SetChatAgent(context.Context, string, string, string, string) error {
	return nil
}
func (commandTestStore) DeleteChatAgent(context.Context, string, string, string) error { return nil }
func (commandTestStore) GetSetting(context.Context, string) (string, error)            { return "", nil }
func (commandTestStore) SetSetting(context.Context, string, string) error              { return nil }
func (commandTestStore) Snapshot(context.Context, string) (*config.Snapshot, error)    { return nil, nil }
func (commandTestStore) SeedNewOrg(context.Context, string) error                      { return nil }

func setupCommandTestStellaHome(t *testing.T) string {
	t.Helper()
	stellaHome := t.TempDir()
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = binaries.EnsureTools(stellaHome)
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	return stellaHome
}

func TestNewRunnerFactoryGo(t *testing.T) {
	setupCommandTestStellaHome(t)
	snap := &config.Snapshot{
		AgentID:  "test-agent",
		Provider: "anthropic",
		Model:    "test-model",
		APIKey:   "test-key",
		Runner:   config.RunnerConfig{Type: "go"},
	}
	snap.Workspace = t.TempDir()

	factory, err := agent.NewRunnerFactory(agent.RunnerFactoryConfig{
		Snap:                  snap,
		ProviderStreamBuilder: testProviderStreamBuilder,
	})
	if err != nil {
		t.Fatalf("NewRunnerFactory: %v", err)
	}

	r, err := factory(context.Background(), agent.RunnerParams{UserID: "1"})
	if err != nil {
		t.Skipf("factory: docker not available: %v", err)
	}

	if r == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestNewRunnerFactoryUnknown(t *testing.T) {
	snap := &config.Snapshot{
		Runner: config.RunnerConfig{Type: "invalid"},
	}

	_, err := agent.NewRunnerFactory(agent.RunnerFactoryConfig{
		Snap:                  snap,
		ProviderStreamBuilder: testProviderStreamBuilder,
	})
	if err == nil {
		t.Fatal("expected error for unknown runner type")
	}
	if !strings.Contains(err.Error(), "unknown runner type") {
		t.Errorf("error = %q, want contains 'unknown runner type'", err.Error())
	}
}

func TestCLIUserSkillsDirUsesUserScope(t *testing.T) {
	setupCommandTestStellaHome(t)
	db, err := appdb.OpenDB(config.DBPath())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()
	orgID, err := appdb.EnsureDefaultOrg(context.Background(), db)
	if err != nil {
		t.Fatalf("ensure default org: %v", err)
	}
	store := cfgstore.NewDBStore(db)
	ctx := config.WithOrgID(context.Background(), orgID)
	if err := store.SeedNewOrg(ctx, orgID); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatal("no enabled agents found")
	}
	snap, err := store.Snapshot(ctx, agents[0].ID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dir, err := cliUserSkillsDir(snap)
	if err != nil {
		t.Fatalf("cliUserSkillsDir: %v", err)
	}
	want := filepath.Join(config.StellaHome(), "workspaces", snap.AgentID, "users", "1", ".agents", "skills")
	if dir != want {
		t.Fatalf("cliUserSkillsDir() = %q, want %q", dir, want)
	}
}

func TestRunHelp(t *testing.T) {
	app := newApp()
	err := app.Run([]string{"stella", "--help"})
	if err != nil {
		t.Fatalf("run --help: %v", err)
	}
}

func TestRunHelpShort(t *testing.T) {
	app := newApp()
	err := app.Run([]string{"stella", "-h"})
	if err != nil {
		t.Fatalf("run -h: %v", err)
	}
}
