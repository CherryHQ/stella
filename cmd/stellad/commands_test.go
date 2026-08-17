package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skills"
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
func (commandTestStore) UpsertChannel(context.Context, config.Channel) error { return nil }
func (commandTestStore) CreateChannel(context.Context, config.Channel) error { return nil }
func (commandTestStore) UpdateChannel(context.Context, config.Channel) error { return nil }
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

func (commandTestStore) SetChannelPluginConfig(context.Context, string, string, string, map[string]any) error {
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

// overrideStore serves one stored customization to the startup resolver.
type overrideStore struct {
	commandTestStore
	rows []config.ManifestPluginOverride
}

func (s overrideStore) ListManifestPluginOverrides(context.Context) ([]config.ManifestPluginOverride, error) {
	return s.rows, nil
}

// Startup is what hands the plugin host its manifest and what the binary
// reconcile installs from. Applying only the enable flag here — which is what it
// used to do — made every definition customization and every admin-added plugin
// evaporate on restart, while the settings page kept showing the merged view.
func TestStartupResolvesDefinitionOverridesAndAddedPlugins(t *testing.T) {
	ctx := context.Background()
	disabled := false
	store := overrideStore{rows: []config.ManifestPluginOverride{
		{PluginID: "tool/tap-web", Config: `{"$sparse":true,"display_name":"Tap (ours)"}`},
		{PluginID: "tool/gh", Enabled: &disabled},
		{
			PluginID: "tool/my-cli",
			Enabled:  &[]bool{true}[0],
			Config:   `{"kind":"tool","name":"my-cli","display_name":"My CLI","description":""}`,
		},
	}}

	manifest, err := loadBuiltinManifestWithOverrides(ctx, store)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	byID := make(map[string]manifestplugins.ManifestPlugin, len(manifest.Plugins))
	for _, p := range manifest.Plugins {
		byID[p.ID] = p
	}

	if got := byID["tool/tap-web"]; got.DisplayName != "Tap (ours)" || !slices.Equal(got.OverriddenFields, []string{"display_name"}) {
		t.Errorf("customized builtin = %q (overridden=%v), want the stored edit", got.DisplayName, got.OverriddenFields)
	}
	if got, ok := byID["tool/my-cli"]; !ok || got.DisplayName != "My CLI" {
		t.Errorf("admin-added plugin missing from the startup manifest: %#v", got)
	}
	if got := byID["tool/gh"]; got.Enabled {
		t.Error("the enable override stopped being applied")
	}
}

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

func TestEnsureEmbeddedAssetsBlocksLegacySkillWithoutMutation(t *testing.T) {
	stellaHome := setupCommandTestStellaHome(t)
	retiredBinary := filepath.Join(stellaHome, "bin", "stella")
	if err := os.WriteFile(retiredBinary, []byte("retired binary"), 0o755); err != nil {
		t.Fatalf("write retired binary: %v", err)
	}
	retired := filepath.Join(stellaHome, ".agents", "skills", "system", "kreuzberg")
	if err := os.MkdirAll(retired, 0o755); err != nil {
		t.Fatalf("create retired skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(retired, "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write retired skill: %v", err)
	}

	if err := ensureEmbeddedAssets(); err == nil {
		t.Fatal("ensureEmbeddedAssets accepted legacy custom skill")
	} else {
		for _, instruction := range []string{
			"system/kreuzberg",
			"back up the listed paths",
			"previous working Stella binary",
			"Settings → Skills",
			"Admin Console → Deployment resources → Global Skills",
			"verify each import",
			"remove only migrated or residual legacy paths",
			"then retry",
		} {
			if !strings.Contains(err.Error(), instruction) {
				t.Errorf("ensureEmbeddedAssets() error = %q, want instruction %q", err, instruction)
			}
		}
	}
	if content, err := os.ReadFile(filepath.Join(retired, "SKILL.md")); err != nil || string(content) != "stale" {
		t.Fatalf("legacy skill mutated: %q, %v", content, err)
	}
	if content, err := os.ReadFile(retiredBinary); err != nil || string(content) != "retired binary" {
		t.Fatalf("legacy gate mutated retired binary: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(stellaHome, "bundles")); !os.IsNotExist(err) {
		t.Fatalf("legacy gate installed a bundle: %v", err)
	}
}

func TestSetupRunsLegacySkillGateBeforeEmbeddedPostgresMutation(t *testing.T) {
	stellaHome := setupCommandTestStellaHome(t)
	retired := filepath.Join(stellaHome, ".agents", "skills", "system", "kreuzberg")
	if err := os.MkdirAll(retired, 0o755); err != nil {
		t.Fatalf("create retired skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(retired, "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write retired skill: %v", err)
	}

	if _, err := setup(t.Context(), config.ServerConfig{}, ""); err == nil {
		t.Fatal("setup accepted legacy custom skill")
	}
	for _, name := range []string{"postgres", "pg-runtime", "bundles"} {
		if _, err := os.Stat(filepath.Join(stellaHome, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy gate allowed %s mutation: %v", name, err)
		}
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

type blockingProjectReconciler struct{ started, release chan struct{} }

func (r blockingProjectReconciler) ReconcileProjectCoordinates(context.Context) (home.ProjectCoordinateReconcileResult, error) {
	close(r.started)
	<-r.release
	return home.ProjectCoordinateReconcileResult{}, nil
}

type blockingSkillReconciler struct{ started, release chan struct{} }

func (r blockingSkillReconciler) ReconcileStartup(context.Context) (skills.SkillStartupReconcileResult, error) {
	close(r.started)
	<-r.release
	return skills.SkillStartupReconcileResult{}, nil
}

func TestLegacyStorageReconciliationNeverBlocksSetup(t *testing.T) {
	projectStarted, skillStarted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	scheduled := make(chan struct{})
	go func() {
		reconcileProjectCoordinatesInBackground(t.Context(), &wg, blockingProjectReconciler{started: projectStarted, release: release})
		reconcileSkillHomeInBackground(t.Context(), &wg, blockingSkillReconciler{started: skillStarted, release: release})
		close(scheduled)
	}()
	select {
	case <-scheduled:
	case <-time.After(time.Second):
		t.Fatal("background reconciliation blocked setup")
	}
	for name, started := range map[string]<-chan struct{}{"project": projectStarted, "Skill": skillStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s reconciliation did not start", name)
		}
	}
	close(release)
	wg.Wait()
}

func TestNativeServerUnsupportedPlatformFailsBeforeConfiguration(t *testing.T) {
	original := nativeServerGOOS
	nativeServerGOOS = "windows"
	t.Cleanup(func() { nativeServerGOOS = original })
	c := ucli.NewContext(ucli.NewApp(), flag.NewFlagSet("server", flag.ContinueOnError), nil)
	err := serverAction(c)
	if err == nil || !strings.Contains(err.Error(), "supported only on Linux and macOS") {
		t.Fatalf("unsupported server error = %v", err)
	}
	upgradeCtx := ucli.NewContext(ucli.NewApp(), flag.NewFlagSet("upgrade", flag.ContinueOnError), nil)
	if err := upgradeCommand().Action(upgradeCtx); err == nil || !strings.Contains(err.Error(), "supported only on Linux and macOS") {
		t.Fatalf("unsupported upgrade error = %v", err)
	}
}
