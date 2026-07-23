package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/pluginhost"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/resources/binaries"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type commandTestProvider struct{}

func (commandTestProvider) API() string { return "anthropic" }
func (commandTestProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("not implemented")
}

func TestWebhookSecretCipherAvoidsTypedNilVault(t *testing.T) {
	var vaultSvc *vault.Service
	if cipher := webhookSecretCipher(vaultSvc); cipher != nil {
		t.Fatalf("webhook cipher = %#v, want nil", cipher)
	}
	_ = webhookSecretCipher(nil)
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
func (commandTestStore) CreateProvider(context.Context, config.Provider) error { return nil }
func (commandTestStore) UpdateProvider(context.Context, config.Provider) error { return nil }
func (commandTestStore) DeleteProvider(context.Context, string) error          { return nil }
func (commandTestStore) ListCachedModels(context.Context) ([]config.CachedModel, error) {
	return nil, nil
}
func (commandTestStore) ReplaceCachedModels(context.Context, string, []string) error { return nil }
func (commandTestStore) Seed(context.Context) error                                  { return nil }
func (commandTestStore) ListAgents(context.Context) ([]config.Agent, error)          { return nil, nil }
func (commandTestStore) ListEnabledAgents(context.Context) ([]config.Agent, error)   { return nil, nil }
func (commandTestStore) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{}, nil
}
func (commandTestStore) CreateAgent(context.Context, config.Agent) error { return nil }
func (commandTestStore) UpdateAgent(context.Context, config.Agent) error { return nil }
func (commandTestStore) DeleteAgent(context.Context, string) error       { return nil }
func (commandTestStore) ListAccessibleAgents(context.Context, string) ([]config.Agent, error) {
	return nil, nil
}
func (commandTestStore) ListChannels(context.Context) ([]config.Channel, error) { return nil, nil }
func (commandTestStore) ListChannelsByType(context.Context, string) ([]config.Channel, error) {
	return nil, nil
}

func (commandTestStore) GetChannel(context.Context, string) (config.Channel, error) {
	return config.Channel{}, nil
}
func (commandTestStore) CreateChannel(context.Context, config.Channel) error { return nil }
func (commandTestStore) UpsertChannel(context.Context, config.Channel) error { return nil }
func (commandTestStore) DeleteChannel(context.Context, string) error         { return nil }
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

func TestEnsureEmbeddedAssetsReplacesRetiredKreuzbergSkill(t *testing.T) {
	stellaHome := setupCommandTestStellaHome(t)
	retired := filepath.Join(stellaHome, ".agents", "skills", "system", "kreuzberg")
	if err := os.MkdirAll(retired, 0o755); err != nil {
		t.Fatalf("create retired skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(retired, "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write retired skill: %v", err)
	}

	if err := ensureEmbeddedAssets(); err != nil {
		t.Fatalf("ensureEmbeddedAssets: %v", err)
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("retired skill still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stellaHome, ".agents", "skills", "system", "xberg", "SKILL.md")); err != nil {
		t.Fatalf("Xberg skill missing: %v", err)
	}
}

func TestCLIUserSkillsDirUsesUserScope(t *testing.T) {
	setupCommandTestStellaHome(t)
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	ctx := context.Background()
	if err := store.Seed(ctx); err != nil {
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
	want := filepath.Join(config.StellaHome(), "users", "1", "data", ".agents", "skills")
	if dir != want {
		t.Fatalf("cliUserSkillsDir() = %q, want %q", dir, want)
	}
}

func TestRunHelp(t *testing.T) {
	app := newApp()
	err := app.Run([]string{"stellad", "--help"})
	if err != nil {
		t.Fatalf("run --help: %v", err)
	}
}

func TestRunHelpShort(t *testing.T) {
	app := newApp()
	err := app.Run([]string{"stellad", "-h"})
	if err != nil {
		t.Fatalf("run -h: %v", err)
	}
}
