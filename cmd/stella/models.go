package main

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// collectModelsFromStore builds the list of available provider/model pairs
// using the Store and models cache.
func collectModelsFromStore(ctx context.Context, store config.Store, snap *config.Snapshot) []pkgchannel.ModelOption {
	collector := newModelOptionCollector()
	collector.Add(snap.Provider, snap.Model)

	if cache, err := config.LoadModelsCache(); err == nil {
		for _, model := range cache.Models {
			collector.Add(model.Provider, model.Model)
		}
		return collector.Models()
	}

	providers, err := store.ListProviders(ctx)
	if err == nil {
		for _, provider := range providers {
			collector.Add(provider.ID, snap.Model)
		}
	}

	return collector.Models()
}

// openStore is a helper that opens the DB and returns a Store.
func openStore() (config.Store, error) {
	db, err := appdb.OpenDB(config.DBPath())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		return nil, fmt.Errorf("seed defaults: %w", err)
	}
	return store, nil
}

// defaultSnapshot returns a snapshot for the first enabled agent.
func defaultSnapshot(ctx context.Context, store config.Store) (*config.Snapshot, error) {
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil || len(agents) == 0 {
		return nil, fmt.Errorf("no enabled agents found")
	}
	return store.Snapshot(ctx, agents[0].ID)
}

type modelOptionCollector struct {
	seen   map[string]bool
	models []pkgchannel.ModelOption
}

func newModelOptionCollector() *modelOptionCollector {
	return &modelOptionCollector{seen: make(map[string]bool)}
}

func (c *modelOptionCollector) Add(provider, model string) {
	key := provider + "/" + model
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.models = append(c.models, pkgchannel.ModelOption{Provider: provider, Model: model})
}

func (c *modelOptionCollector) Models() []pkgchannel.ModelOption {
	return c.models
}
