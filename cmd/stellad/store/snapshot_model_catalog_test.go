package store_test

import (
	"testing"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestSnapshotUsesCatalogPriceForReferencedModelWithoutOverride(t *testing.T) {
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	ctx := t.Context()
	if err := store.CreateProvider(ctx, config.Provider{ID: "deepseek-provider", Type: "openai", Name: "DeepSeek", Enabled: true, APIKey: "sk-test", CatalogID: "deepseek"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(ctx, config.Agent{ID: "catalog-agent", Name: "Catalog Agent", Model: "deepseek-provider/deepseek-v4-flash", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx, "catalog-agent")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := snapshot.ModelCosts[config.ModelKey{Provider: "deepseek-provider", Model: "deepseek-v4-flash"}]
	if !ok || !got.Configured() || got.Input != 0.14 || got.Output != 0.28 {
		t.Fatalf("catalog model cost = %#v, present=%v", got, ok)
	}
}

func TestSnapshotCarriesConfiguredOutputLimit(t *testing.T) {
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	ctx := t.Context()
	if err := store.CreateProvider(ctx, config.Provider{ID: "gateway", Type: "openai-response", Name: "Gateway", Enabled: true, APIKey: "test", Models: map[string]config.ProviderModelOverride{"deepseek/deepseek-v4-flash": {Enabled: config.ValuePtr(true), MaxTokens: config.ValuePtr(384000)}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(ctx, config.Agent{ID: "limit-agent", Name: "Limit", Model: "gateway/deepseek/deepseek-v4-flash", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx, "limit-agent")
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ModelTokenLimit("gateway", "deepseek/deepseek-v4-flash"); got != 384000 {
		t.Fatalf("output limit = %d, want 384000", got)
	}
}
