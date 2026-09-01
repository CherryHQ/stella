package store_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
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
