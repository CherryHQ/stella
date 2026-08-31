// Package modelresolve owns the one effective-model merge used by the control
// plane and runtime snapshot. Catalog data is a lower layer, fetched IDs add
// discovery, and sparse provider overrides win field by field.
package modelresolve

import (
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/modelcatalog"
	"github.com/CherryHQ/stella/pkg/ai"
)

type Result struct {
	Model    config.ProviderModel
	Source   string
	Override *config.ProviderModelOverride
	Catalog  *modelcatalog.Model
	Found    bool
}

// Resolve merges catalog metadata, fetched discovery, and a provider override.
// The fetched layer currently carries IDs only, so it contributes discovery
// and enabled defaults while catalog metadata remains the value source.
func Resolve(provider config.Provider, modelID string, fetched bool, catalog *modelcatalog.Catalog) Result {
	catalogModel, inCatalog := catalog.Model(provider.CatalogID, modelID)
	override, hasOverride := provider.Models[modelID]
	found := inCatalog || fetched || hasOverride

	model := config.ProviderModel{ID: modelID, Name: modelID, Enabled: provider.ModelPolicy != "allowlist"}
	if inCatalog {
		model.Name = catalogModel.Name
		model.Reasoning = catalogModel.Reasoning
		model.Input = append([]string(nil), catalogModel.Modalities.Input...)
		model.Output = append([]string(nil), catalogModel.Modalities.Output...)
		model.ContextWindow = catalogModel.Limit.Context
		model.MaxTokens = catalogModel.Limit.Output
		if catalogModel.Cost != nil {
			model.Cost = catalogCost(*catalogModel.Cost)
		}
	}
	if hasOverride {
		applyOverride(&model, override)
	}
	source := "custom"
	if fetched {
		source = "fetched"
	} else if inCatalog {
		source = "catalog"
	}
	return Result{Model: model, Source: source, Override: overridePtr(provider.Models, modelID, hasOverride), Catalog: catalogPtr(catalogModel, inCatalog), Found: found}
}

func applyOverride(model *config.ProviderModel, override config.ProviderModelOverride) {
	if override.Name != nil {
		model.Name = *override.Name
	}
	if override.Enabled != nil {
		model.Enabled = *override.Enabled
	}
	if override.Reasoning != nil {
		model.Reasoning = *override.Reasoning
	}
	if override.Input != nil {
		model.Input = append([]string(nil), (*override.Input)...)
	}
	if override.Output != nil {
		model.Output = append([]string(nil), (*override.Output)...)
	}
	if override.ContextWindow != nil {
		model.ContextWindow = *override.ContextWindow
	}
	if override.MaxTokens != nil {
		model.MaxTokens = *override.MaxTokens
	}
	if override.Cost != nil {
		model.Cost = *override.Cost
	}
}

// RuntimeCost materializes omitted config rates into a runtime cost. In
// particular, an omitted reasoning rate inherits output while an explicit zero
// remains free.
func RuntimeCost(c config.ProviderModelCost) ai.ModelCost {
	rates := ai.ModelRates{}
	if c.Input != nil {
		rates.Input = *c.Input
	}
	if c.Output != nil {
		rates.Output = *c.Output
	}
	if c.CacheRead != nil {
		rates.CacheRead = *c.CacheRead
	}
	if c.CacheWrite != nil {
		rates.CacheWrite = *c.CacheWrite
	}
	if c.Reasoning != nil {
		rates.Reasoning = *c.Reasoning
	} else {
		rates.Reasoning = rates.Output
	}
	if c.InputAudio != nil {
		rates.InputAudio = *c.InputAudio
	}
	if c.OutputAudio != nil {
		rates.OutputAudio = *c.OutputAudio
	}
	out := ai.ModelCost{ModelRates: rates, Priced: true}
	for _, tier := range c.Tiers {
		t := ai.ModelCostTier{MinContext: tier.MinContext, ModelRates: rates}
		if tier.Input != nil {
			t.Input = *tier.Input
		}
		if tier.Output != nil {
			t.Output = *tier.Output
		}
		if tier.CacheRead != nil {
			t.CacheRead = *tier.CacheRead
		}
		if tier.CacheWrite != nil {
			t.CacheWrite = *tier.CacheWrite
		}
		if tier.Reasoning != nil {
			t.Reasoning = *tier.Reasoning
		}
		if tier.InputAudio != nil {
			t.InputAudio = *tier.InputAudio
		}
		if tier.OutputAudio != nil {
			t.OutputAudio = *tier.OutputAudio
		}
		out.Tiers = append(out.Tiers, t)
	}
	return out
}

func catalogCost(cost modelcatalog.ModelCost) config.ProviderModelCost {
	out := config.ProviderModelCost{Input: cost.Input, Output: cost.Output, CacheRead: cost.CacheRead, CacheWrite: cost.CacheWrite, Reasoning: cost.Reasoning, InputAudio: cost.InputAudio, OutputAudio: cost.OutputAudio}
	for _, tier := range cost.Tiers {
		out.Tiers = append(out.Tiers, config.ProviderModelCostTier{MinContext: tier.MinContext, Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite, Reasoning: tier.Reasoning, InputAudio: tier.InputAudio})
	}
	return out
}

func overridePtr(models map[string]config.ProviderModelOverride, id string, ok bool) *config.ProviderModelOverride {
	if !ok {
		return nil
	}
	value := models[id]
	return &value
}

func catalogPtr(model modelcatalog.Model, ok bool) *modelcatalog.Model {
	if !ok {
		return nil
	}
	return &model
}
