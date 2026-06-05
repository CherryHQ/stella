package main

import (
	"context"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// collectModelsFromStore builds the list of available provider/model pairs
// using the Store and models cache.
func collectModelsFromStore(ctx context.Context, store config.Store) []pkgchannel.ModelOption {
	collector := newModelOptionCollector()

	if cache, err := config.LoadModelsCache(); err == nil {
		for _, model := range cache.Models {
			collector.Add(model.Provider, model.Model)
		}
		return collector.Models()
	}

	providers, err := store.ListProviders(ctx)
	if err == nil {
		for _, provider := range providers {
			collector.Add(provider.ID, "")
		}
	}

	return collector.Models()
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
