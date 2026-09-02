package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/db/txlock"
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

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %v, want one Stella agent", agents)
	}
	if got := agents[0]; got.ID != store.DefaultStellaAgentID || got.Name != "Stella" || !got.Enabled || got.Model != "" || !got.SystemSettingsToolsEnabled {
		t.Errorf("seeded agent = %+v, want enabled Stella with Settings tools and an empty model", got)
	}

	providers, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("providers = %v, want none", providers)
	}
	channels, err := s.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 0 {
		t.Errorf("channels = %v, want none", channels)
	}
}

func TestAgentSystemSettingsToolsEnabledPersistsThroughUpdates(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	created := config.Agent{ID: "settings-tools", Name: "Settings tools", Enabled: true}
	if err := s.CreateAgent(ctx, created); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetAgent(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SystemSettingsToolsEnabled {
		t.Fatalf("new Agent settings-tools flag = true, want default false")
	}
	stored.SystemSettingsToolsEnabled = true
	if err := s.UpdateAgent(ctx, stored); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetAgentSnapshot(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Agent.SystemSettingsToolsEnabled {
		t.Fatal("ordinary Agent update did not persist settings-tools flag")
	}
	snapshot.Agent.SystemSettingsToolsEnabled = false
	if _, err := s.UpdateAgentIfVersion(ctx, snapshot.Agent, snapshot.Version); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetAgent(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SystemSettingsToolsEnabled {
		t.Fatal("conditional Agent update did not persist settings-tools flag")
	}
}

func TestSeedRemovesLegacyTracePlugin(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.UpsertPlugin(ctx, config.Plugin{ID: "hook/trace", Kind: config.PluginKindHook, Name: "trace", Enabled: true}); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	overrides, err := s.ListPluginOverrides(ctx)
	if err != nil {
		t.Fatalf("ListPluginOverrides: %v", err)
	}
	for _, override := range overrides {
		if override.ID == "hook/trace" {
			t.Fatal("legacy trace plugin was not removed")
		}
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

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "stella" {
		t.Errorf("agents after double Seed = %v, want one stella", agents)
	}
}

func TestSeedConcurrent(t *testing.T) {
	s := setupDBStore(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			errs <- s.Seed(testCtx())
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Seed: %v", err)
		}
	}

	agents, err := s.ListAgents(testCtx())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "stella" {
		t.Fatalf("agents after concurrent Seed = %v, want one stella", agents)
	}
	providers, err := s.ListProviders(testCtx())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("providers after concurrent Seed = %v, want none", providers)
	}
	channels, err := s.ListChannels(testCtx())
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 0 {
		t.Errorf("channels after concurrent Seed = %v, want none", channels)
	}
}

func TestSeedPreservesExistingAgent(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.CreateAgent(ctx, config.Agent{ID: "existing-agent", Name: "Existing", Model: "test/model", Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "existing-agent" {
		t.Fatalf("agents after Seed = %v, want only existing agent", agents)
	}
}

func TestSeedAgentConflictDoesNotOverwriteExistingAgent(t *testing.T) {
	_, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	q := sqlc.New(db)

	first := sqlc.SeedAgentParams{
		ID:        "stella",
		Name:      "first",
		Model:     "test/model",
		Workspace: "/tmp/first",
		Sandbox:   json.RawMessage(`{}`),
		Scope:     config.AgentScopeSystem,
		Enabled:   true,
	}
	if err := q.SeedAgent(ctx, first); err != nil {
		t.Fatalf("seed first agent: %v", err)
	}
	second := first
	second.Name = "second"
	second.Model = "other/model"
	second.Enabled = false
	if err := q.SeedAgent(ctx, second); err != nil {
		t.Fatalf("seed conflicting agent: %v", err)
	}

	var name, model string
	var enabled bool
	if err := db.QueryRow(ctx, `SELECT name, model, enabled FROM agent WHERE id = 'stella'`).Scan(&name, &model, &enabled); err != nil {
		t.Fatalf("get seeded agent: %v", err)
	}
	if name != "first" || model != "test/model" || !enabled {
		t.Errorf("conflicting seed overwrote agent: name=%q model=%q enabled=%t", name, model, enabled)
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

// TestProviderSnapshotPreventsStaleMixedOverwrite exercises the exact Settings
// interleaving: a tool reads its editable projection, an admin changes that
// row, then the tool tries to write its old projection with the observed token.
// The snapshot binds the original fields to their original version, so the CAS
// must reject the stale overwrite rather than accept a mixed old-fields/new-token
// pair.
func TestProviderSnapshotPreventsStaleMixedOverwrite(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	initial := config.Provider{
		ID: "provider", Name: "Provider", BaseURL: "https://provider.example.test",
		Models: map[string]config.ProviderModelOverride{"old": {Enabled: config.ValuePtr(true)}},
	}
	if err := s.CreateProvider(ctx, initial); err != nil {
		t.Fatalf("create provider: %v", err)
	}

	snapshot, err := s.GetProviderSnapshot(ctx, initial.ID)
	if err != nil {
		t.Fatalf("read provider snapshot: %v", err)
	}
	if _, ok := snapshot.Provider.Models["old"]; !ok || snapshot.Version == "" {
		t.Fatalf("snapshot = %#v, want old models and version", snapshot)
	}

	concurrent := snapshot.Provider
	concurrent.Models = map[string]config.ProviderModelOverride{"new": {Enabled: config.ValuePtr(true)}}
	if err := s.UpdateProvider(ctx, concurrent); err != nil {
		t.Fatalf("concurrent update: %v", err)
	}
	// Make the interleaving deterministic even on a database with coarse clock
	// resolution: the simulated admin write is strictly newer than the snapshot.
	if _, err := db.Exec(ctx, `UPDATE provider SET updated_at = updated_at + interval '1 second' WHERE id = $1`, initial.ID); err != nil {
		t.Fatalf("advance concurrent version: %v", err)
	}

	updated, err := s.UpdateProviderIfVersion(ctx, snapshot.Provider, snapshot.Version)
	if err != nil {
		t.Fatalf("stale conditional update: %v", err)
	}
	if updated {
		t.Fatal("stale snapshot overwrite succeeded")
	}
	got, err := s.GetProvider(ctx, initial.ID)
	if err != nil {
		t.Fatalf("get provider after rejected overwrite: %v", err)
	}
	if _, ok := got.Models["new"]; !ok {
		t.Fatalf("concurrent models were overwritten: %#v", got.Models)
	}
}

func TestProviderCustomModels(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	wantModels := map[string]config.ProviderModelOverride{
		"qwen3.6-plus": {
			Name:          config.ValuePtr("Qwen3.6 Plus"),
			Enabled:       config.ValuePtr(true),
			Reasoning:     config.ValuePtr(true),
			Input:         config.ValuePtr([]string{"text", "image"}),
			Output:        config.ValuePtr([]string{"text"}),
			ContextWindow: config.ValuePtr(1000000),
			MaxTokens:     config.ValuePtr(65536),
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
	if got.Models["qwen3.6-plus"].Name == nil || *got.Models["qwen3.6-plus"].Name != "Qwen3.6 Plus" {
		t.Fatalf("custom model missing: %+v", got.Models)
	}
	if got.Models["qwen3.6-plus"].Enabled == nil || !*got.Models["qwen3.6-plus"].Enabled {
		t.Fatalf("custom model should be enabled: %+v", got.Models["qwen3.6-plus"])
	}

	got.Models["qwen3.5-plus"] = config.ProviderModelOverride{Name: config.ValuePtr("Qwen3.5 Plus"), Enabled: config.ValuePtr(false)}
	if err := s.UpdateProvider(ctx, got); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	updated, err := s.GetProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if updated.Models["qwen3.5-plus"].Name == nil || *updated.Models["qwen3.5-plus"].Name != "Qwen3.5 Plus" {
		t.Fatalf("updated model missing: %+v", updated.Models)
	}
	if updated.Models["qwen3.5-plus"].Enabled == nil || *updated.Models["qwen3.5-plus"].Enabled {
		t.Fatalf("updated model should remain disabled: %+v", updated.Models["qwen3.5-plus"])
	}
}

func TestProviderCredentialsIgnoreEnvironment(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	for key, value := range map[string]string{
		"ANTHROPIC_API_KEY":  "anthropic-env-key",
		"ANTHROPIC_BASE_URL": "https://anthropic.env.example.com",
		"OPENAI_API_KEY":     "openai-env-key",
		"OPENAI_BASE_URL":    "https://openai.env.example.com",
	} {
		t.Setenv(key, value)
	}
	if err := s.CreateProvider(ctx, config.Provider{ID: "anthropic-empty", Type: "anthropic", Name: "Anthropic Empty"}); err != nil {
		t.Fatalf("CreateProvider(anthropic-empty): %v", err)
	}
	if err := s.CreateProvider(ctx, config.Provider{ID: "openai-configured", Type: "openai", Name: "OpenAI Configured", APIKey: "db-key", BaseURL: "https://db.example.com"}); err != nil {
		t.Fatalf("CreateProvider(openai-configured): %v", err)
	}
	for _, agent := range []config.Agent{
		{ID: "empty", Name: "Empty", Model: "anthropic-empty/claude", Enabled: true},
		{ID: "configured", Name: "Configured", Model: "openai-configured/gpt", Enabled: true},
	} {
		if err := s.CreateAgent(ctx, agent); err != nil {
			t.Fatalf("CreateAgent(%s): %v", agent.ID, err)
		}
	}

	providers, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	listed := make(map[string]config.Provider, len(providers))
	for _, provider := range providers {
		listed[provider.ID] = provider
	}
	if got := listed["anthropic-empty"]; got.APIKey != "" || got.BaseURL != "" {
		t.Errorf("ListProviders empty credentials = (%q, %q), want empty DB values", got.APIKey, got.BaseURL)
	}
	if got := listed["openai-configured"]; got.APIKey != "db-key" || got.BaseURL != "https://db.example.com" {
		t.Errorf("ListProviders configured credentials = (%q, %q), want DB values", got.APIKey, got.BaseURL)
	}

	for _, want := range []config.Provider{
		{ID: "anthropic-empty"},
		{ID: "openai-configured", APIKey: "db-key", BaseURL: "https://db.example.com"},
	} {
		got, err := s.GetProvider(ctx, want.ID)
		if err != nil {
			t.Fatalf("GetProvider(%s): %v", want.ID, err)
		}
		if got.APIKey != want.APIKey || got.BaseURL != want.BaseURL {
			t.Errorf("GetProvider(%s) credentials = (%q, %q), want (%q, %q)", want.ID, got.APIKey, got.BaseURL, want.APIKey, want.BaseURL)
		}
	}

	for _, want := range []struct {
		agentID string
		apiKey  string
		baseURL string
	}{
		{agentID: "empty"},
		{agentID: "configured", apiKey: "db-key", baseURL: "https://db.example.com"},
	} {
		snap, err := s.Snapshot(ctx, want.agentID)
		if err != nil {
			t.Fatalf("Snapshot(%s): %v", want.agentID, err)
		}
		if snap.APIKey != want.apiKey || snap.BaseURL != want.baseURL {
			t.Errorf("Snapshot(%s) credentials = (%q, %q), want (%q, %q)", want.agentID, snap.APIKey, snap.BaseURL, want.apiKey, want.baseURL)
		}
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

func TestUpdateAgentIfVersionRejectsStaleWrite(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	a := config.Agent{ID: "versioned", Name: "Versioned", Model: "anthropic/model", Scope: config.AgentScopeSystem, Enabled: true}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetAgentSnapshot(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := snapshot.Version
	a.Name = "fresh"
	newVersion, err := s.UpdateAgentIfVersion(ctx, a, version)
	if err != nil || newVersion == version {
		t.Fatalf("UpdateAgentIfVersion = (%q, %v)", newVersion, err)
	}
	a.Name = "stale"
	if _, err := s.UpdateAgentIfVersion(ctx, a, version); !errors.Is(err, config.ErrAgentVersionConflict) {
		t.Fatalf("stale UpdateAgentIfVersion error = %v, want conflict", err)
	}
	got, err := s.GetAgent(ctx, a.ID)
	if err != nil || got.Name != "fresh" {
		t.Fatalf("stored agent = %+v, %v", got, err)
	}
}

// TestAgentSnapshotPreventsStaleMixedMutations exercises the former tool
// interleaving: it reads an editable Agent, an admin updates it, and then the
// tool tries to update or delete using the old projection. A snapshot's version
// is from the same row as its fields, so neither exact CAS can accept it.
func TestAgentSnapshotPreventsStaleMixedMutations(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	initial := config.Agent{ID: "snapshot-agent", Name: "before", Model: "anthropic/model", Scope: config.AgentScopeSystem, Enabled: true}
	if err := s.CreateAgent(ctx, initial); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	snapshot, err := s.GetAgentSnapshot(ctx, initial.ID)
	if err != nil {
		t.Fatalf("read agent snapshot: %v", err)
	}
	if snapshot.Agent.Name != "before" || snapshot.Version == "" {
		t.Fatalf("snapshot = %#v, want original agent and version", snapshot)
	}

	admin := snapshot.Agent
	admin.Name = "admin change"
	if err := s.UpdateAgent(ctx, admin); err != nil {
		t.Fatalf("concurrent admin update: %v", err)
	}
	// Keep the interleaving deterministic on databases whose now() precision is
	// coarser than two adjacent test statements.
	if _, err := db.Exec(ctx, `UPDATE agent SET updated_at = updated_at + interval '1 second' WHERE id = $1`, initial.ID); err != nil {
		t.Fatalf("advance concurrent version: %v", err)
	}

	stale := snapshot.Agent
	stale.Name = "tool overwrite"
	if _, err := s.UpdateAgentIfVersion(ctx, stale, snapshot.Version); !errors.Is(err, config.ErrAgentVersionConflict) {
		t.Fatalf("stale conditional update error = %v, want conflict", err)
	}
	version, err := time.Parse(time.RFC3339Nano, snapshot.Version)
	if err != nil {
		t.Fatalf("parse snapshot version: %v", err)
	}
	if _, err := sqlc.New(db).DeleteAgentIfVersion(ctx, sqlc.DeleteAgentIfVersionParams{ID: initial.ID, UpdatedAt: version}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale conditional delete error = %v, want no rows", err)
	}
	got, err := s.GetAgent(ctx, initial.ID)
	if err != nil || got.Name != "admin change" {
		t.Fatalf("admin mutation was not preserved: %+v, %v", got, err)
	}
}

// TestUpdateAgentIfVersionAndAssignCreatorIsAtomic proves that the sole Agent
// scope transition which needs a creator assignment cannot leave that relation
// behind when the conditional Agent update loses its CAS race.
func TestUpdateAgentIfVersionAndAssignCreatorIsAtomic(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	const creatorID = "00000000-0000-0000-0000-000000000001"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, creatorID, "creator@example.test"); err != nil {
		t.Fatalf("create creator: %v", err)
	}

	initial := config.Agent{ID: "scope-transition", Name: "before", Model: "anthropic/model", Scope: config.AgentScopeSystem, CreatorID: creatorID, Enabled: true}
	if err := s.CreateAgent(ctx, initial); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	snapshot, err := s.GetAgentSnapshot(ctx, initial.ID)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	transition := snapshot.Agent
	transition.Scope = config.AgentScopeRestricted
	if _, err := s.UpdateAgentIfVersionAndAssignCreator(ctx, transition, snapshot.Version, creatorID); err != nil {
		t.Fatalf("successful scope transition: %v", err)
	}
	var assigned bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_user_agent WHERE user_id = $1 AND agent_id = $2)`, creatorID, initial.ID).Scan(&assigned); err != nil {
		t.Fatalf("read creator assignment: %v", err)
	}
	if !assigned {
		t.Fatal("successful scope transition did not assign creator")
	}

	stale := config.Agent{ID: "stale-scope-transition", Name: "before", Model: "anthropic/model", Scope: config.AgentScopeSystem, CreatorID: creatorID, Enabled: true}
	if err := s.CreateAgent(ctx, stale); err != nil {
		t.Fatalf("create stale agent: %v", err)
	}
	staleSnapshot, err := s.GetAgentSnapshot(ctx, stale.ID)
	if err != nil {
		t.Fatalf("read stale snapshot: %v", err)
	}
	concurrent := staleSnapshot.Agent
	concurrent.Name = "concurrent admin write"
	if err := s.UpdateAgent(ctx, concurrent); err != nil {
		t.Fatalf("concurrent update: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE agent SET updated_at = updated_at + interval '1 second' WHERE id = $1`, stale.ID); err != nil {
		t.Fatalf("advance concurrent version: %v", err)
	}
	staleTransition := staleSnapshot.Agent
	staleTransition.Scope = config.AgentScopeRestricted
	if _, err := s.UpdateAgentIfVersionAndAssignCreator(ctx, staleTransition, staleSnapshot.Version, creatorID); !errors.Is(err, config.ErrAgentVersionConflict) {
		t.Fatalf("stale scope transition = %v, want version conflict", err)
	}
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_user_agent WHERE user_id = $1 AND agent_id = $2)`, creatorID, stale.ID).Scan(&assigned); err != nil {
		t.Fatalf("read stale creator assignment: %v", err)
	}
	if assigned {
		t.Fatal("stale scope transition restored creator assignment")
	}
	got, err := s.GetAgent(ctx, stale.ID)
	if err != nil || got.Scope != config.AgentScopeSystem || got.Name != concurrent.Name {
		t.Fatalf("stale scope transition changed Agent: %#v, %v", got, err)
	}
}

// TestScopeTransitionRejectsPriorAssignmentRevoke proves that the relation
// revision is part of the tool's Agent version: a revocation committed before a
// stale system→restricted request prevents the request from recreating access.
func TestScopeTransitionRejectsPriorAssignmentRevoke(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	const creatorID = "00000000-0000-0000-0000-000000000003"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, creatorID, "prior-revoke-creator@example.test"); err != nil {
		t.Fatalf("create creator: %v", err)
	}
	initial := config.Agent{ID: "scope-prior-revoke", Name: "before", Model: "anthropic/model", Scope: config.AgentScopeSystem, CreatorID: creatorID, Enabled: true}
	if err := s.CreateAgent(ctx, initial); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	authStore := appdb.NewAuthStore(db)
	if err := authStore.AssignAgent(ctx, creatorID, initial.ID); err != nil {
		t.Fatalf("seed creator assignment: %v", err)
	}
	snapshot, err := s.GetAgentSnapshot(ctx, initial.ID)
	if err != nil {
		t.Fatalf("read tool snapshot: %v", err)
	}
	if err := authStore.RemoveAgent(ctx, creatorID, initial.ID); err != nil {
		t.Fatalf("revoke creator assignment: %v", err)
	}

	transition := snapshot.Agent
	transition.Scope = config.AgentScopeRestricted
	if _, err := s.UpdateAgentIfVersionAndAssignCreator(ctx, transition, snapshot.Version, creatorID); !errors.Is(err, config.ErrAgentVersionConflict) {
		t.Fatalf("transition after revoke = %v, want version conflict", err)
	}
	var assigned bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_user_agent WHERE user_id = $1 AND agent_id = $2)`, creatorID, initial.ID).Scan(&assigned); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if assigned {
		t.Fatal("stale system-to-restricted transition recreated the prior revoke")
	}
	got, err := s.GetAgent(ctx, initial.ID)
	if err != nil || got.Scope != config.AgentScopeSystem {
		t.Fatalf("stale scope transition changed Agent: %#v, %v", got, err)
	}
}

// TestScopeTransitionAndConcurrentRevocationSerializesAssignment exercises the
// interleaving that used to re-grant a revoked assignment: the transition has
// performed its conditional Agent write, and an administrator revokes before
// the transition can commit its assignment. Both transactions take the shared
// relation lock, so the revoke runs after the transition and is the final state.
func TestScopeTransitionAndConcurrentRevocationSerializesAssignment(t *testing.T) {
	s, db := setupDBStoreWithDB(t)
	ctx := testCtx()
	const creatorID = "00000000-0000-0000-0000-000000000002"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, creatorID, "revoked-creator@example.test"); err != nil {
		t.Fatalf("create creator: %v", err)
	}
	initial := config.Agent{ID: "scope-revoke-race", Name: "before", Model: "anthropic/model", Scope: config.AgentScopeSystem, CreatorID: creatorID, Enabled: true}
	if err := s.CreateAgent(ctx, initial); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	snapshot, err := s.GetAgentSnapshot(ctx, initial.ID)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	expected, err := time.Parse(time.RFC3339Nano, snapshot.Version)
	if err != nil {
		t.Fatalf("parse snapshot version: %v", err)
	}

	transition, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin scope transition: %v", err)
	}
	defer transition.Rollback(ctx) //nolint:errcheck // commit below makes rollback inert
	if err := txlock.AdvisoryXactLock(ctx, transition, txlock.AgentAssignmentLockKey(creatorID, initial.ID)); err != nil {
		t.Fatalf("lock assignment relation: %v", err)
	}
	var updated time.Time
	if err := transition.QueryRow(ctx, `UPDATE agent SET scope = 'restricted', updated_at = now() WHERE id = $1 AND updated_at = $2 RETURNING updated_at`, initial.ID, expected).Scan(&updated); err != nil {
		t.Fatalf("conditional scope update: %v", err)
	}
	if _, err := transition.Exec(ctx, `INSERT INTO auth_user_agent (user_id, agent_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, creatorID, initial.ID); err != nil {
		t.Fatalf("assign creator during transition: %v", err)
	}

	authStore := appdb.NewAuthStore(db)
	revoked := make(chan error, 1)
	go func() { revoked <- authStore.RemoveAgent(ctx, creatorID, initial.ID) }()
	// The revocation must wait on the exact relation lock, rather than slip
	// between the CAS and INSERT as it did before the lock was shared.
	select {
	case err := <-revoked:
		t.Fatalf("revocation completed before scope transition released its lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := transition.Commit(ctx); err != nil {
		t.Fatalf("commit scope transition: %v", err)
	}
	if err := <-revoked; err != nil {
		t.Fatalf("revoke creator assignment: %v", err)
	}

	var assigned bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_user_agent WHERE user_id = $1 AND agent_id = $2)`, creatorID, initial.ID).Scan(&assigned); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if assigned {
		t.Fatal("successful system-to-restricted transition restored concurrent administrative revoke")
	}
	got, err := s.GetAgent(ctx, initial.ID)
	if err != nil || got.Scope != config.AgentScopeRestricted {
		t.Fatalf("successful scope transition = %#v, %v", got, err)
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

	if err := s.CreateProvider(ctx, config.Provider{ID: "anthropic", Type: "anthropic", Name: "Anthropic", APIKey: "sk-test", Enabled: true}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
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

func TestSnapshotCarriesDeclaredModelInput(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	if err := s.CreateProvider(ctx, config.Provider{
		ID:     "claude",
		Type:   "anthropic",
		Name:   "Claude",
		APIKey: "sk-claude",
		Models: map[string]config.ProviderModelOverride{
			"claude-sonnet-4-6": {Enabled: config.ValuePtr(true), Input: config.ValuePtr([]string{"text", "image"})},
			"claude-text-only":  {Enabled: config.ValuePtr(true), Input: config.ValuePtr([]string{"text"})},
			"claude-undeclared": {Enabled: config.ValuePtr(true)},
		},
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	// Referenced by the unique-type alias, not the provider ID, so the snapshot
	// must key model inputs the same way it keys credentials.
	if err := s.CreateAgent(ctx, config.Agent{
		ID:          "stella",
		Name:        "Stella",
		Model:       "anthropic/claude-sonnet-4-6",
		ModelStrong: "anthropic/claude-text-only",
		ModelFast:   "anthropic/claude-undeclared",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	snap, err := s.Snapshot(ctx, "stella")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if got := snap.ResolveModelTier(config.ModelTierStrong).ImageCapability(); got != ai.ImageUnsupported {
		t.Errorf("strong tier ImageCapability = %v, want ImageUnsupported", got)
	}
	if got := snap.ResolveModelTier(config.ModelTierFast).ImageCapability(); got != ai.ImageUnknown {
		t.Errorf("fast tier ImageCapability = %v, want ImageUnknown", got)
	}
	if got := snap.ModelInput("anthropic", "claude-sonnet-4-6"); len(got) != 2 {
		t.Errorf("ModelInput(anthropic, claude-sonnet-4-6) = %v, want [text image]", got)
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

	telegram, err := s.GetPlugin(ctx, "channel/telegram")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if telegram.Enabled {
		t.Error("channel/telegram should default to disabled")
	}

	if err := s.SetPluginEnabled(ctx, "channel/telegram", true); err != nil {
		t.Fatalf("SetPluginEnabled: %v", err)
	}
	telegram, _ = s.GetPlugin(ctx, "channel/telegram")
	if !telegram.Enabled {
		t.Error("expected channel/telegram to be enabled after override")
	}

	overrides, err := s.ListPluginOverrides(ctx)
	if err != nil {
		t.Fatalf("ListPluginOverrides: %v", err)
	}
	if len(overrides) != 1 || overrides[0].ID != "channel/telegram" {
		t.Errorf("expected 1 override for channel/telegram, got %d", len(overrides))
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
