package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/store"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func testCtx() context.Context {
	return context.Background()
}

func setupDBStore(t *testing.T) *store.DBStore {
	t.Helper()
	s, _ := setupDBStoreWithDB(t)
	return s
}

func setupDBStoreWithDB(t *testing.T) (*store.DBStore, *pgxpool.Pool) {
	t.Helper()
	db := dbtest.New(t)
	s := store.NewDBStore(db)
	return s, db
}

func TestNewDBStorePreservesDBPoolPolicy(t *testing.T) {
	_, db := setupDBStoreWithDB(t)

	if got := db.Stat().MaxConns(); got < 4 {
		t.Fatalf("MaxConns = %d, want >= 4", got)
	}
}

func TestSeed(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	providers, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != len(config.BuiltinProviderNames) {
		t.Errorf("expected %d providers, got %d", len(config.BuiltinProviderNames), len(providers))
	}
	found := false
	for _, p := range providers {
		if p.Enabled {
			t.Errorf("provider %q should be disabled until an admin enables it", p.ID)
		}
		if p.Type == "anthropic" {
			found = true
		}
	}
	if !found {
		t.Error("expected anthropic provider to be seeded")
	}

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "Stella" {
		t.Errorf("expected 1 Stella agent, got %v", agents)
	}
	if !agents[0].Enabled {
		t.Error("stella agent should be enabled")
	}
}

func TestSeedUsesConfiguredProviderInstanceForAgentModel(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.CreateProvider(ctx, config.Provider{ID: "claude", Type: "anthropic", Name: "Claude"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Model != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("agent.Model = %q, want %q", agents[0].Model, "anthropic/claude-sonnet-4-6")
	}
}

func TestSeedIdempotent(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.Seed(ctx); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatalf("second Seed: %v", err)
	}

	providers, _ := s.ListProviders(ctx)
	if len(providers) != len(config.BuiltinProviderNames) {
		t.Errorf("expected %d providers after double seed, got %d", len(config.BuiltinProviderNames), len(providers))
	}

	for _, p := range providers {
		if p.ID != p.Type {
			t.Errorf("provider %q should have deterministic ID equal to type, got ID=%q", p.Type, p.ID)
		}
	}
}

func TestProviderCRUD(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	p := config.Provider{ID: "openai", Name: "OpenAI", APIKey: "sk-test", BaseURL: "https://api.openai.com"}
	if err := s.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	got, err := s.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Name != "OpenAI" || got.APIKey != "sk-test" {
		t.Errorf("GetProvider = %+v", got)
	}

	p.Name = "OpenAI Updated"
	p.APIKey = "sk-new"
	if err := s.UpdateProvider(ctx, p); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	got, _ = s.GetProvider(ctx, "openai")
	if got.Name != "OpenAI Updated" || got.APIKey != "sk-new" {
		t.Errorf("after update: %+v", got)
	}

	if err := s.DeleteProvider(ctx, "openai"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	providers, _ := s.ListProviders(ctx)
	for _, pr := range providers {
		if pr.ID == "openai" {
			t.Error("provider should be deleted")
		}
	}
}

func TestProviderCustomModels(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	wantModels := map[string]config.ProviderModel{
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
	p := config.Provider{
		ID:     "openai",
		Name:   "OpenAI",
		APIKey: "sk-test",
		Models: wantModels,
	}
	if err := s.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	got, err := s.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Models["qwen3.6-plus"].Name != "Qwen3.6 Plus" {
		t.Fatalf("custom model missing: %+v", got.Models)
	}
	if !got.Models["qwen3.6-plus"].Enabled {
		t.Fatalf("custom model should be enabled: %+v", got.Models["qwen3.6-plus"])
	}

	got.Models["qwen3.5-plus"] = config.ProviderModel{ID: "qwen3.5-plus", Name: "Qwen3.5 Plus", Enabled: false}
	if err := s.UpdateProvider(ctx, got); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	updated, err := s.GetProvider(ctx, "openai")
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
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.CreateProvider(ctx, config.Provider{ID: "anthropic", Name: "Anthropic"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example.com")

	got, err := s.GetProvider(ctx, "anthropic")
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
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.CreateProvider(ctx, config.Provider{ID: "openai", Name: "OpenAI"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "openai-env-key")
	got, err := s.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.APIKey != "openai-env-key" {
		t.Errorf("APIKey = %q, want env fallback", got.APIKey)
	}
}

func TestProviderEnvNoOverwrite(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.CreateProvider(ctx, config.Provider{ID: "anthropic", Name: "Anthropic", APIKey: "db-key"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	got, _ := s.GetProvider(ctx, "anthropic")
	if got.APIKey != "db-key" {
		t.Errorf("APIKey = %q, want DB value preserved over env", got.APIKey)
	}
}

func TestAgentCRUD(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	a := config.Agent{
		ID:           "coder",
		Name:         "Coder",
		Model:        "anthropic/claude-sonnet-4-6",
		ModelStrong:  "anthropic/claude-opus-4-6",
		ModelFast:    "anthropic/claude-haiku-4-5",
		SystemPrompt: "You code.",
		Workspace:    "/tmp/coder",
		Enabled:      true,
	}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := s.GetAgent(ctx, "coder")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != "Coder" || !got.Enabled || got.ModelStrong != "anthropic/claude-opus-4-6" {
		t.Errorf("GetAgent = %+v", got)
	}

	a.Name = "Coder Updated"
	a.Enabled = false
	if err := s.UpdateAgent(ctx, a); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	got, _ = s.GetAgent(ctx, "coder")
	if got.Name != "Coder Updated" || got.Enabled {
		t.Errorf("after update: %+v", got)
	}

	if err := s.DeleteAgent(ctx, "coder"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	agents, _ := s.ListAgents(ctx)
	for _, ag := range agents {
		if ag.ID == "coder" {
			t.Error("agent should be deleted")
		}
	}
}

func TestListEnabledAgents(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	_ = s.CreateAgent(ctx, config.Agent{ID: "a1", Name: "A1", Model: "anthropic/m", Enabled: true})
	_ = s.CreateAgent(ctx, config.Agent{ID: "a2", Name: "A2", Model: "anthropic/m", Enabled: false})

	enabled, err := s.ListEnabledAgents(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAgents: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ID != "a1" {
		t.Errorf("expected only a1 enabled, got %v", enabled)
	}
}

func TestChannelCRUD(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	ch := config.Channel{ID: "telegram", Enabled: true, Config: `{"token":"abc"}`}
	if err := s.UpsertChannel(ctx, ch); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	got, err := s.GetChannel(ctx, "telegram")
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if !got.Enabled || got.Config != `{"token":"abc"}` {
		t.Errorf("GetChannel = %+v", got)
	}

	// Upsert update.
	ch.Config = `{"token":"xyz"}`
	ch.Enabled = false
	if err := s.UpsertChannel(ctx, ch); err != nil {
		t.Fatalf("UpsertChannel update: %v", err)
	}
	got, _ = s.GetChannel(ctx, "telegram")
	if got.Enabled || got.Config != `{"token":"xyz"}` {
		t.Errorf("after upsert: %+v", got)
	}

	channels, err := s.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(channels))
	}
}

func TestChannelBindingIsAtomic(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	if err := s.CreateAgent(ctx, config.Agent{ID: "agent-1", Name: "Agent", Model: "anthropic/test", Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"telegram-a", "telegram-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			errs <- s.UpsertChannel(ctx, config.Channel{ID: id, Type: "telegram", AgentID: "agent-1", Enabled: true, Config: `{}`})
		}(id)
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, conflicted int
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		var conflict *config.ChannelBindingConflictError
		if errors.As(err, &conflict) {
			conflicted++
			continue
		}
		t.Fatalf("UpsertChannel error = %v", err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("successes=%d conflicts=%d, want exactly one of each", succeeded, conflicted)
	}
}

func TestChatAgentCRUD(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	_ = s.CreateAgent(ctx, config.Agent{ID: "agent1", Name: "A1", Model: "p/m", Enabled: true})

	if err := s.SetChatAgent(ctx, "telegram", "telegram", "group-42", "agent1"); err != nil {
		t.Fatalf("SetChatAgent: %v", err)
	}

	agentID, err := s.GetChatAgent(ctx, "telegram", "telegram", "group-42")
	if err != nil {
		t.Fatalf("GetChatAgent: %v", err)
	}
	if agentID != "agent1" {
		t.Errorf("agentID = %q, want %q", agentID, "agent1")
	}

	if err := s.DeleteChatAgent(ctx, "telegram", "telegram", "group-42"); err != nil {
		t.Fatalf("DeleteChatAgent: %v", err)
	}

	_, err = s.GetChatAgent(ctx, "telegram", "telegram", "group-42")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestSettings(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	val, err := s.GetSetting(ctx, "runner")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty, got %q", val)
	}

	if err := s.SetSetting(ctx, "runner", `{"type":"go"}`); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	val, _ = s.GetSetting(ctx, "runner")
	if val != `{"type":"go"}` {
		t.Errorf("setting = %q", val)
	}

	_ = s.SetSetting(ctx, "runner", `{"type":"docker"}`)
	val, _ = s.GetSetting(ctx, "runner")
	if val != `{"type":"docker"}` {
		t.Errorf("setting = %q", val)
	}
}

func TestSnapshot(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	_ = s.Seed(ctx)

	agents, err := s.ListAgents(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatalf("ListAgents: %v (count=%d)", err, len(agents))
	}
	stellaID := agents[0].ID

	providers, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	var anthropicID string
	for _, p := range providers {
		if p.Type == "anthropic" {
			anthropicID = p.ID
			break
		}
	}
	_ = s.UpdateProvider(ctx, config.Provider{ID: anthropicID, Type: "anthropic", Name: "Anthropic", APIKey: "sk-test", Enabled: true})
	_ = s.UpdateAgent(ctx, config.Agent{
		ID:           stellaID,
		Name:         "Stella",
		Model:        "anthropic/claude-sonnet-4-6",
		ModelStrong:  "anthropic/claude-opus-4-6",
		SystemPrompt: "You are Stella.",
		Workspace:    "/tmp/stella",
		Sandbox: config.SandboxConfig{
			Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkAllowAll},
		},
		Enabled: true,
	})

	_ = s.SetSetting(ctx, "runner", `{"type":"go","idle_timeout":30}`)
	_ = s.SetSetting(ctx, "compaction", `{"enabled":true}`)

	_ = s.UpsertPlugin(ctx, config.Plugin{
		ID:      "tool/custom",
		Kind:    config.PluginKindTool,
		Name:    "custom",
		Enabled: true,
		Config:  map[string]any{"mode": "test"},
	})

	snap, err := s.Snapshot(ctx, stellaID)
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
	if snap.Runner.IdleTimeout != 30 {
		t.Errorf("Runner.IdleTimeout = %d", snap.Runner.IdleTimeout)
	}
	if snap.Sandbox.NetworkMode() != config.SandboxNetworkAllowAll {
		t.Errorf("Sandbox.NetworkMode() = %q", snap.Sandbox.NetworkMode())
	}
	if len(snap.Plugins) != len(config.BuiltinPluginIDs())+1 {
		t.Errorf("expected %d plugins, got %d", len(config.BuiltinPluginIDs())+1, len(snap.Plugins))
	}
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
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.CreateProvider(ctx, config.Provider{ID: "claude", Type: "anthropic", Name: "Claude", APIKey: "sk-claude"}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := s.CreateAgent(ctx, config.Agent{ID: "stella", Name: "Stella", Model: "anthropic/claude-sonnet-4-6", Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	snap, err := s.Snapshot(ctx, "stella")
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
	s := setupDBStore(t)
	ctx := testCtx()

	_ = s.Seed(ctx)
	_ = s.CreateAgent(ctx, config.Agent{ID: "a", Name: "A", Model: "anthropic/m", Enabled: true})

	snap, err := s.Snapshot(ctx, "a")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Runner.IdleTimeout != 10 {
		t.Errorf("default Runner.IdleTimeout = %d, want 10", snap.Runner.IdleTimeout)
	}
}

func TestSnapshotNotFound(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	_, err := s.Snapshot(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestCreateAgentRejectsInvalidSandbox(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	err := s.CreateAgent(ctx, config.Agent{
		ID:      "bad-sandbox",
		Name:    "Bad Sandbox",
		Model:   "anthropic/m",
		Enabled: true,
		Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: "bogus"}},
	})
	if err == nil {
		t.Fatal("expected invalid sandbox config error")
	}
}

func TestGetProviderNotFound(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	_, err := s.GetProvider(ctx, "nope")
	if err == nil {
		t.Error("expected error")
	}
}

func TestGetAgentNotFound(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	_, err := s.GetAgent(ctx, "nope")
	if err == nil {
		t.Error("expected error")
	}
}

func TestPluginCRUD(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	p := config.Plugin{
		ID:      "tool/read",
		Kind:    config.PluginKindTool,
		Name:    "read",
		Enabled: true,
		Config:  map[string]any{"timeout": float64(30)},
	}
	if err := s.UpsertPlugin(ctx, p); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}

	got, err := s.GetPlugin(ctx, "tool/read")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if got.Kind != config.PluginKindTool || got.Name != "read" || !got.Enabled {
		t.Errorf("GetPlugin = %+v", got)
	}
	if got.Config["timeout"] != float64(30) {
		t.Errorf("Config[timeout] = %v", got.Config["timeout"])
	}

	builtinCount := len(config.BuiltinPlugins())
	all, err := s.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(all) != builtinCount+1 {
		t.Errorf("expected %d plugins (builtins + 1), got %d", builtinCount+1, len(all))
	}

	if err := s.SetPluginEnabled(ctx, "tool/read", false); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	got, _ = s.GetPlugin(ctx, "tool/read")
	if got.Enabled {
		t.Error("expected disabled")
	}
	if got.Config["timeout"] != float64(30) {
		t.Errorf("SetPluginEnabled should preserve config, got %+v", got.Config)
	}

	newCfg := map[string]any{"timeout": float64(60), "verbose": true}
	if err := s.SetPluginConfig(ctx, "tool/read", newCfg); err != nil {
		t.Fatalf("SetPluginConfig: %v", err)
	}
	got, _ = s.GetPlugin(ctx, "tool/read")
	if got.Config["timeout"] != float64(60) || got.Config["verbose"] != true {
		t.Errorf("Config after update = %+v", got.Config)
	}
	if got.Enabled {
		t.Error("SetPluginConfig should preserve enabled=false")
	}

	tools, err := s.ListPluginsByKind(ctx, config.PluginKindTool)
	if err != nil {
		t.Fatalf("ListPluginsByKind: %v", err)
	}
	foundRead := false
	for _, t2 := range tools {
		if t2.ID == "tool/read" {
			foundRead = true
		}
	}
	if !foundRead {
		t.Error("ListPluginsByKind(tool) should include tool/read")
	}

	if err := s.DeletePlugin(ctx, "tool/read"); err != nil {
		t.Fatalf("DeletePlugin: %v", err)
	}
	_, err = s.GetPlugin(ctx, "tool/read")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestPluginBuiltinsWithoutSeed(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	plugins, err := s.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != len(config.BuiltinPluginIDs()) {
		t.Fatalf("expected %d built-in plugins, got %d", len(config.BuiltinPluginIDs()), len(plugins))
	}

	have := make(map[string]bool)
	for _, p := range plugins {
		have[p.ID] = true
	}
	for _, id := range config.BuiltinPluginIDs() {
		if !have[id] {
			t.Errorf("missing built-in plugin %q", id)
		}
	}
}

func TestPluginBuiltinOverrides(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	webfetch, err := s.GetPlugin(ctx, "tool/webfetch")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if webfetch.Enabled {
		t.Error("tool/webfetch should default to disabled")
	}

	if err := s.SetPluginEnabled(ctx, "tool/webfetch", true); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	webfetch, _ = s.GetPlugin(ctx, "tool/webfetch")
	if !webfetch.Enabled {
		t.Error("expected tool/webfetch to be enabled after override")
	}

	overrides, err := s.ListPluginOverrides(ctx)
	if err != nil {
		t.Fatalf("ListPluginOverrides: %v", err)
	}
	if len(overrides) != 1 || overrides[0].ID != "tool/webfetch" {
		t.Errorf("expected 1 override for tool/webfetch, got %d", len(overrides))
	}

	plugins, err := s.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins) != len(config.BuiltinPluginIDs()) {
		t.Errorf("expected %d plugins, got %d", len(config.BuiltinPluginIDs()), len(plugins))
	}
}

func TestPluginBuiltinChannelDefaults(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	p, err := s.GetPlugin(ctx, "channel/telegram")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if p.Enabled {
		t.Error("telegram plugin should default to disabled")
	}
	if len(p.Config) != 0 {
		t.Errorf("expected channel plugin config empty, got %+v", p.Config)
	}
}

func TestGetChannelNotFound(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	_, err := s.GetChannel(ctx, "nope")
	if err == nil {
		t.Error("expected error")
	}
}
