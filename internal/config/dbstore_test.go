package config

import (
	"context"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
)

const testOrgID = "test-org-id"

func testCtx() context.Context {
	return WithOrgID(context.Background(), testOrgID)
}

func setupDBStore(t *testing.T) *DBStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`INSERT OR IGNORE INTO auth_organization (id, name, source) VALUES (?, ?, ?)`, testOrgID, "Test Org", "test")
	if err != nil {
		t.Fatalf("create test org: %v", err)
	}
	store := NewDBStore(db)
	store.defaultOrgID = testOrgID
	return store
}

func TestSeedDefaults(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	providers, err := store.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != len(builtinProviderNames) {
		t.Errorf("expected %d providers, got %d", len(builtinProviderNames), len(providers))
	}
	// Anthropic should be present.
	found := false
	for _, p := range providers {
		if p.ID == "anthropic" {
			found = true
		}
	}
	if !found {
		t.Error("expected anthropic provider to be seeded")
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "stella" {
		t.Errorf("expected 1 stella agent, got %v", agents)
	}
	if !agents[0].Enabled {
		t.Error("stella agent should be enabled")
	}
}

func TestSeedDefaultsUsesConfiguredProviderInstanceForAgentModel(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.CreateProvider(ctx, Provider{ID: "claude", Type: "anthropic", Name: "Claude"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	agent, err := store.GetAgent(ctx, "stella")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.Model != "claude/claude-sonnet-4-6" {
		t.Fatalf("agent.Model = %q, want %q", agent.Model, "claude/claude-sonnet-4-6")
	}
}

func TestSeedDefaultsIdempotent(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("first SeedDefaults: %v", err)
	}
	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("second SeedDefaults: %v", err)
	}

	providers, _ := store.ListProviders(ctx)
	if len(providers) != len(builtinProviderNames) {
		t.Errorf("expected %d providers after double seed, got %d", len(builtinProviderNames), len(providers))
	}
}

func TestProviderCRUD(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	p := Provider{ID: "openai", Name: "OpenAI", APIKey: "sk-test", BaseURL: "https://api.openai.com"}
	if err := store.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	got, err := store.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Name != "OpenAI" || got.APIKey != "sk-test" {
		t.Errorf("GetProvider = %+v", got)
	}

	p.Name = "OpenAI Updated"
	p.APIKey = "sk-new"
	if err := store.UpdateProvider(ctx, p); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	got, _ = store.GetProvider(ctx, "openai")
	if got.Name != "OpenAI Updated" || got.APIKey != "sk-new" {
		t.Errorf("after update: %+v", got)
	}

	if err := store.DeleteProvider(ctx, "openai"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	providers, _ := store.ListProviders(ctx)
	for _, pr := range providers {
		if pr.ID == "openai" {
			t.Error("provider should be deleted")
		}
	}
}

func TestProviderCustomModels(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	wantModels := map[string]ProviderModel{
		"qwen3.6-plus": {
			ID:            "qwen3.6-plus",
			Name:          "Qwen3.6 Plus",
			Enabled:       true,
			Reasoning:     true,
			Input:         []string{"text", "image"},
			Output:        []string{"text"},
			ContextWindow: 1000000,
			MaxTokens:     65536,
		},
	}
	p := Provider{
		ID:     "openai",
		Name:   "OpenAI",
		APIKey: "sk-test",
		Models: wantModels,
	}
	if err := store.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	got, err := store.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Models["qwen3.6-plus"].Name != "Qwen3.6 Plus" {
		t.Fatalf("custom model missing: %+v", got.Models)
	}
	if !got.Models["qwen3.6-plus"].Enabled {
		t.Fatalf("custom model should be enabled: %+v", got.Models["qwen3.6-plus"])
	}

	got.Models["qwen3.5-plus"] = ProviderModel{ID: "qwen3.5-plus", Name: "Qwen3.5 Plus", Enabled: false}
	if err := store.UpdateProvider(ctx, got); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	updated, err := store.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if updated.Models["qwen3.5-plus"].Name != "Qwen3.5 Plus" {
		t.Fatalf("updated model missing: %+v", updated.Models)
	}
	if updated.Models["qwen3.5-plus"].Enabled {
		t.Fatalf("updated model should remain disabled: %+v", updated.Models["qwen3.5-plus"])
	}
}

func TestProviderEnvFallback(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.CreateProvider(ctx, Provider{ID: "anthropic", Name: "Anthropic"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example.com")

	got, err := store.GetProvider(ctx, "anthropic")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.APIKey != "env-key" {
		t.Errorf("APIKey = %q, want env fallback", got.APIKey)
	}
	if got.BaseURL != "https://env.example.com" {
		t.Errorf("BaseURL = %q, want env fallback", got.BaseURL)
	}
}

func TestProviderEnvFallbackOpenAI(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.CreateProvider(ctx, Provider{ID: "openai", Name: "OpenAI"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "openai-env-key")
	got, err := store.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.APIKey != "openai-env-key" {
		t.Errorf("APIKey = %q, want env fallback", got.APIKey)
	}
}

func TestProviderEnvNoOverwrite(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.CreateProvider(ctx, Provider{ID: "anthropic", Name: "Anthropic", APIKey: "db-key"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	got, _ := store.GetProvider(ctx, "anthropic")
	if got.APIKey != "db-key" {
		t.Errorf("APIKey = %q, want DB value preserved over env", got.APIKey)
	}
}

func TestAgentCRUD(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	a := Agent{
		ID:           "coder",
		Name:         "Coder",
		Model:        "anthropic/claude-sonnet-4-6",
		ModelStrong:  "anthropic/claude-opus-4-6",
		ModelFast:    "anthropic/claude-haiku-4-5",
		SystemPrompt: "You code.",
		Workspace:    "/tmp/coder",
		Enabled:      true,
	}
	if err := store.CreateAgent(ctx, a); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := store.GetAgent(ctx, "coder")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != "Coder" || !got.Enabled || got.ModelStrong != "anthropic/claude-opus-4-6" {
		t.Errorf("GetAgent = %+v", got)
	}

	a.Name = "Coder Updated"
	a.Enabled = false
	if err := store.UpdateAgent(ctx, a); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	got, _ = store.GetAgent(ctx, "coder")
	if got.Name != "Coder Updated" || got.Enabled {
		t.Errorf("after update: %+v", got)
	}

	if err := store.DeleteAgent(ctx, "coder"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	agents, _ := store.ListAgents(ctx)
	for _, ag := range agents {
		if ag.ID == "coder" {
			t.Error("agent should be deleted")
		}
	}
}

func TestListEnabledAgents(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	_ = store.CreateAgent(ctx, Agent{ID: "a1", Name: "A1", Model: "anthropic/m", Enabled: true})
	_ = store.CreateAgent(ctx, Agent{ID: "a2", Name: "A2", Model: "anthropic/m", Enabled: false})

	enabled, err := store.ListEnabledAgents(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAgents: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ID != "a1" {
		t.Errorf("expected only a1 enabled, got %v", enabled)
	}
}

func TestChannelCRUD(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	ch := Channel{ID: "telegram", Enabled: true, Config: `{"token":"abc"}`}
	if err := store.UpsertChannel(ctx, ch); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	got, err := store.GetChannel(ctx, "telegram")
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if !got.Enabled || got.Config != `{"token":"abc"}` {
		t.Errorf("GetChannel = %+v", got)
	}

	// Upsert update.
	ch.Config = `{"token":"xyz"}`
	ch.Enabled = false
	if err := store.UpsertChannel(ctx, ch); err != nil {
		t.Fatalf("UpsertChannel update: %v", err)
	}
	got, _ = store.GetChannel(ctx, "telegram")
	if got.Enabled || got.Config != `{"token":"xyz"}` {
		t.Errorf("after upsert: %+v", got)
	}

	channels, err := store.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(channels))
	}
}

func TestChatAgentCRUD(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	_ = store.CreateAgent(ctx, Agent{ID: "agent1", Name: "A1", Model: "p/m", Enabled: true})

	if err := store.SetChatAgent(ctx, "telegram", "telegram", "group-42", "agent1"); err != nil {
		t.Fatalf("SetChatAgent: %v", err)
	}

	agentID, err := store.GetChatAgent(ctx, "telegram", "telegram", "group-42")
	if err != nil {
		t.Fatalf("GetChatAgent: %v", err)
	}
	if agentID != "agent1" {
		t.Errorf("agentID = %q, want %q", agentID, "agent1")
	}

	if err := store.DeleteChatAgent(ctx, "telegram", "telegram", "group-42"); err != nil {
		t.Fatalf("DeleteChatAgent: %v", err)
	}

	_, err = store.GetChatAgent(ctx, "telegram", "telegram", "group-42")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestSettings(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	// Empty by default.
	val, err := store.GetSetting(ctx, "runner")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty, got %q", val)
	}

	if err := store.SetSetting(ctx, "runner", `{"type":"go"}`); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	val, _ = store.GetSetting(ctx, "runner")
	if val != `{"type":"go"}` {
		t.Errorf("setting = %q", val)
	}

	// Overwrite.
	_ = store.SetSetting(ctx, "runner", `{"type":"docker"}`)
	val, _ = store.GetSetting(ctx, "runner")
	if val != `{"type":"docker"}` {
		t.Errorf("setting = %q", val)
	}
}

func TestSnapshot(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	// Seed first (matches real startup order), then configure.
	_ = store.SeedDefaults(ctx, testOrgID)
	_ = store.UpdateProvider(ctx, Provider{ID: "anthropic", Name: "Anthropic", APIKey: "sk-test"})
	_ = store.UpdateAgent(ctx, Agent{
		ID:           "stella",
		Name:         "Stella",
		Model:        "anthropic/claude-sonnet-4-6",
		ModelStrong:  "anthropic/claude-opus-4-6",
		SystemPrompt: "You are Stella.",
		Workspace:    "/tmp/stella",
		Sandbox: SandboxConfig{
			Network: SandboxNetworkConfig{Mode: SandboxNetworkAllowAll},
		},
		Enabled: true,
	})

	_ = store.SetSetting(ctx, "runner", `{"type":"go","idle_timeout":30}`)
	_ = store.SetSetting(ctx, "compaction", `{"enabled":true}`)

	// Add a custom plugin to verify it appears in the snapshot.
	_ = store.UpsertPlugin(ctx, Plugin{
		ID:      "tool/custom",
		Kind:    PluginKindTool,
		Name:    "custom",
		Enabled: true,
		Config:  map[string]any{"mode": "test"},
	})

	snap, err := store.Snapshot(ctx, "stella")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if snap.Provider != "anthropic" {
		t.Errorf("Provider = %q", snap.Provider)
	}
	if snap.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("Model = %q", snap.Model)
	}
	if snap.ModelStrong != "anthropic/claude-opus-4-6" {
		t.Errorf("ModelStrong = %q", snap.ModelStrong)
	}
	if snap.APIKey != "sk-test" {
		t.Errorf("APIKey = %q", snap.APIKey)
	}
	if snap.SystemPrompt != "You are Stella." {
		t.Errorf("SystemPrompt = %q", snap.SystemPrompt)
	}
	if snap.Runner.Type != "go" {
		t.Errorf("Runner.Type = %q", snap.Runner.Type)
	}
	if snap.Runner.IdleTimeout != 30 {
		t.Errorf("Runner.IdleTimeout = %d", snap.Runner.IdleTimeout)
	}
	if snap.Sandbox.NetworkMode() != SandboxNetworkAllowAll {
		t.Errorf("Sandbox.NetworkMode() = %q", snap.Sandbox.NetworkMode())
	}
	// built-in + 1 custom plugin.
	if len(snap.Plugins) != len(BuiltinPluginIDs())+1 {
		t.Errorf("expected %d plugins, got %d", len(BuiltinPluginIDs())+1, len(snap.Plugins))
	}
	// Verify custom plugin is present.
	found := false
	for _, p := range snap.Plugins {
		if p.ID == "tool/custom" && p.Config["mode"] == "test" {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom plugin not found in snapshot")
	}
}

func TestSnapshotResolvesUniqueProviderTypeAlias(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.CreateProvider(ctx, Provider{ID: "claude", Type: "anthropic", Name: "Claude", APIKey: "sk-claude"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := store.CreateAgent(ctx, Agent{ID: "stella", Name: "Stella", Model: "anthropic/claude-sonnet-4-6", Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	snap, err := store.Snapshot(ctx, "stella")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.APIKey != "sk-claude" {
		t.Fatalf("APIKey = %q, want %q", snap.APIKey, "sk-claude")
	}
	creds := snap.ResolveProviderCreds("anthropic")
	if creds.APIKey != "sk-claude" {
		t.Fatalf("ResolveProviderCreds(anthropic).APIKey = %q, want %q", creds.APIKey, "sk-claude")
	}
	model := snap.ResolveModel()
	if model.API != "anthropic" {
		t.Fatalf("ResolveModel().API = %q, want %q", model.API, "anthropic")
	}
	if model.Provider != "anthropic" {
		t.Fatalf("ResolveModel().Provider = %q, want %q", model.Provider, "anthropic")
	}
}

func TestSnapshotDefaults(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	_ = store.SeedDefaults(ctx, testOrgID)
	_ = store.CreateAgent(ctx, Agent{ID: "a", Name: "A", Model: "anthropic/m", Enabled: true})

	snap, err := store.Snapshot(ctx, "a")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Runner.Type != "go" {
		t.Errorf("default Runner.Type = %q, want go", snap.Runner.Type)
	}
	if snap.Runner.IdleTimeout != 10 {
		t.Errorf("default Runner.IdleTimeout = %d, want 10", snap.Runner.IdleTimeout)
	}
}

func TestSnapshotNotFound(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	_, err := store.Snapshot(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestCreateAgentRejectsInvalidSandbox(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	err := store.CreateAgent(ctx, Agent{
		ID:      "bad-sandbox",
		Name:    "Bad Sandbox",
		Model:   "anthropic/m",
		Enabled: true,
		Sandbox: SandboxConfig{Network: SandboxNetworkConfig{Mode: "bogus"}},
	})
	if err == nil {
		t.Fatal("expected invalid sandbox config error")
	}
}

func TestGetProviderNotFound(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	_, err := store.GetProvider(ctx, "nope")
	if err == nil {
		t.Error("expected error")
	}
}

func TestGetAgentNotFound(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	_, err := store.GetAgent(ctx, "nope")
	if err == nil {
		t.Error("expected error")
	}
}

func TestPluginCRUD(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	// Create via Upsert.
	p := Plugin{
		ID:      "tool/read",
		Kind:    PluginKindTool,
		Name:    "read",
		Enabled: true,
		Config:  map[string]any{"timeout": float64(30)},
	}
	if err := store.UpsertPlugin(ctx, p); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}

	// Get.
	got, err := store.GetPlugin(ctx, "tool/read")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if got.Kind != PluginKindTool || got.Name != "read" || !got.Enabled {
		t.Errorf("GetPlugin = %+v", got)
	}
	if got.Config["timeout"] != float64(30) {
		t.Errorf("Config[timeout] = %v", got.Config["timeout"])
	}

	// List.
	all, err := store.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(all))
	}

	// SetEnabled.
	if err := store.SetPluginEnabled(ctx, "tool/read", false); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	got, _ = store.GetPlugin(ctx, "tool/read")
	if got.Enabled {
		t.Error("expected disabled")
	}

	// ListEnabled should be empty now.
	enabled, err := store.ListEnabledPlugins(ctx)
	if err != nil {
		t.Fatalf("ListEnabledPlugins: %v", err)
	}
	if len(enabled) != 0 {
		t.Errorf("expected 0 enabled, got %d", len(enabled))
	}

	// SetConfig.
	newCfg := map[string]any{"timeout": float64(60), "verbose": true}
	if err := store.SetPluginConfig(ctx, "tool/read", newCfg); err != nil {
		t.Fatalf("SetPluginConfig: %v", err)
	}
	got, _ = store.GetPlugin(ctx, "tool/read")
	if got.Config["timeout"] != float64(60) || got.Config["verbose"] != true {
		t.Errorf("Config after update = %+v", got.Config)
	}

	// ListByKind.
	_ = store.UpsertPlugin(ctx, Plugin{ID: "channel/telegram", Kind: PluginKindChannel, Name: "telegram", Enabled: true, Config: map[string]any{}})
	tools, err := store.ListPluginsByKind(ctx, PluginKindTool)
	if err != nil {
		t.Fatalf("ListPluginsByKind: %v", err)
	}
	if len(tools) != 1 || tools[0].ID != "tool/read" {
		t.Errorf("ListPluginsByKind(tool) = %+v", tools)
	}

	// Delete.
	if err := store.DeletePlugin(ctx, "tool/read"); err != nil {
		t.Fatalf("DeletePlugin: %v", err)
	}
	_, err = store.GetPlugin(ctx, "tool/read")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestPluginSeedDefaults(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	plugins, err := store.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != len(BuiltinPluginIDs()) {
		t.Fatalf("expected %d built-in plugins, got %d", len(BuiltinPluginIDs()), len(plugins))
	}

	// Verify all built-in IDs are present.
	have := make(map[string]bool)
	for _, p := range plugins {
		have[p.ID] = true
	}
	for _, id := range BuiltinPluginIDs() {
		if !have[id] {
			t.Errorf("missing built-in plugin %q", id)
		}
	}
}

func TestPluginSeedDefaultsIdempotent(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("first SeedDefaults: %v", err)
	}

	// User modifies plugin/channel state.
	if err := store.SetPluginEnabled(ctx, "tool/webfetch", false); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	if err := store.UpsertChannel(ctx, Channel{ID: "telegram", Type: "telegram", Enabled: true, Config: `{"token":"abc"}`}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	// Second seed should NOT overwrite user changes.
	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("second SeedDefaults: %v", err)
	}

	plugins, err := store.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != len(BuiltinPluginIDs()) {
		t.Errorf("expected %d plugins after double seed, got %d", len(BuiltinPluginIDs()), len(plugins))
	}

	webfetchPlugin, err := store.GetPlugin(ctx, "tool/webfetch")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if webfetchPlugin.Enabled {
		t.Error("expected tool/webfetch to remain disabled after second seed")
	}

	tgChannel, err := store.GetChannel(ctx, "telegram")
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if tgChannel.Config != `{"token":"abc"}` {
		t.Errorf("expected telegram channel config preserved, got %s", tgChannel.Config)
	}

	tgPlugin, err := store.GetPlugin(ctx, "channel/telegram")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if len(tgPlugin.Config) != 0 {
		t.Errorf("expected telegram plugin config cleared, got %+v", tgPlugin.Config)
	}
}

func TestPluginSeedKeepsChannelConfigInSettingsChannels(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	if err := store.UpsertChannel(ctx, Channel{ID: "telegram", Type: "telegram", Enabled: true, Config: `{"token":"tg-123"}`}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	if err := store.SeedDefaults(ctx, testOrgID); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	p, err := store.GetPlugin(ctx, "channel/telegram")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if !p.Enabled {
		t.Error("telegram plugin should be enabled")
	}
	if len(p.Config) != 0 {
		t.Errorf("expected channel plugin config to stay empty, got %+v", p.Config)
	}

	ch, err := store.GetChannel(ctx, "telegram")
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if ch.Config != `{"token":"tg-123"}` {
		t.Errorf("channel config = %s, want token persisted on settings_channel", ch.Config)
	}
}

func TestGetChannelNotFound(t *testing.T) {
	store := setupDBStore(t)
	ctx := testCtx()

	_, err := store.GetChannel(ctx, "nope")
	if err == nil {
		t.Error("expected error")
	}
}
