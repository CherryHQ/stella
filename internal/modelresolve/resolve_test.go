package modelresolve

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/modelcatalog"
)

func TestResolvePrecedenceAndPresence(t *testing.T) {
	input := []string{"text", "image"}
	output := []string{"text"}
	contextWindow := 272000
	catalog := &modelcatalog.Catalog{ProvidersByID: map[string]modelcatalog.Provider{"deepseek": {ID: "deepseek", Models: map[string]modelcatalog.Model{"m": {ID: "m", Name: "Catalog M", Reasoning: true, Modalities: modelcatalog.Modalities{Input: input, Output: output}, Limit: modelcatalog.ModelLimit{Context: 100}, Cost: &modelcatalog.ModelCost{Input: ptr(1.0)}}}}}}
	provider := config.Provider{ID: "p", CatalogID: "deepseek", Models: map[string]config.ProviderModelOverride{"m": {Reasoning: ptr(false), Input: ptr([]string{}), ContextWindow: &contextWindow}}}
	got := Resolve(provider, "m", true, catalog)
	if got.Source != "fetched" || got.Model.Name != "Catalog M" || got.Model.Reasoning || len(got.Model.Input) != 0 || got.Model.ContextWindow != contextWindow {
		t.Fatalf("resolved = %#v", got)
	}
	if got.Override == nil || got.Catalog == nil {
		t.Fatalf("presence projections missing: %#v", got)
	}
}

func TestResolveMergesSparseCostOverrides(t *testing.T) {
	catalog := &modelcatalog.Catalog{ProvidersByID: map[string]modelcatalog.Provider{
		"p": {
			ID: "p",
			Models: map[string]modelcatalog.Model{
				"m": {
					ID: "m",
					Cost: &modelcatalog.ModelCost{
						Input: ptr(1.0), Output: ptr(2.0), CacheRead: ptr(0.2),
						Tiers: []modelcatalog.ModelCostTier{{MinContext: 128000, Input: ptr(1.5), Output: ptr(3.0)}},
					},
				},
			},
		},
	}}
	provider := config.Provider{ID: "p", CatalogID: "p", Models: map[string]config.ProviderModelOverride{
		"m": {Cost: &config.ProviderModelCost{
			Input: ptr(0.0),
			Tiers: []config.ProviderModelCostTier{
				{MinContext: 128000, Output: ptr(4.0)},
				{MinContext: 32000, Input: ptr(1.2)},
			},
		}},
	}}

	cost := Resolve(provider, "m", false, catalog).Model.Cost
	if *cost.Input != 0 || *cost.Output != 2 || *cost.CacheRead != 0.2 {
		t.Fatalf("base cost fields were not preserved: %#v", cost)
	}
	if len(cost.Tiers) != 2 || cost.Tiers[0].MinContext != 32000 || cost.Tiers[1].MinContext != 128000 || *cost.Tiers[1].Input != 1.5 || *cost.Tiers[1].Output != 4 {
		t.Fatalf("tier cost fields were not merged and sorted: %#v", cost.Tiers)
	}
}

func TestResolveMatchesProviderPrefixedIDsAndAllowsManualBinding(t *testing.T) {
	catalog := &modelcatalog.Catalog{ProvidersByID: map[string]modelcatalog.Provider{
		"openai": {
			ID: "openai",
			Models: map[string]modelcatalog.Model{
				"gpt-4o": {ID: "gpt-4o", Name: "GPT-4o", Limit: modelcatalog.ModelLimit{Context: 128000}},
				"o3":     {ID: "o3", Name: "o3", Reasoning: true},
			},
		},
	}}
	provider := config.Provider{ID: "gateway", CatalogID: "openai"}

	automatic := Resolve(provider, "openai/gpt-4o", true, catalog)
	if automatic.Catalog == nil || automatic.Catalog.ID != "gpt-4o" || automatic.Model.ContextWindow != 128000 {
		t.Fatalf("automatic match = %#v", automatic)
	}

	provider.Models = map[string]config.ProviderModelOverride{
		"gateway-reasoner": {CatalogModel: ptr("o3")},
	}
	manual := Resolve(provider, "gateway-reasoner", true, catalog)
	if manual.Catalog == nil || manual.Catalog.ID != "o3" || !manual.Model.Reasoning {
		t.Fatalf("manual match = %#v", manual)
	}

	provider.Models["openai/gpt-4o"] = config.ProviderModelOverride{CatalogModel: ptr("")}
	unmatched := Resolve(provider, "openai/gpt-4o", true, catalog)
	if unmatched.Catalog != nil || unmatched.Model.ContextWindow != 0 {
		t.Fatalf("explicitly unmatched = %#v", unmatched)
	}
}

func TestResolvePolicyAndSources(t *testing.T) {
	catalog := &modelcatalog.Catalog{ProvidersByID: map[string]modelcatalog.Provider{"p": {ID: "p", Models: map[string]modelcatalog.Model{"catalog": {ID: "catalog"}}}}}
	allow := config.Provider{ID: "p", CatalogID: "p", ModelPolicy: "allowlist"}
	if Resolve(allow, "catalog", false, catalog).Model.Enabled {
		t.Fatal("allowlist inherited model enabled")
	}
	allow.Models = map[string]config.ProviderModelOverride{"catalog": {Enabled: ptr(true)}}
	if !Resolve(allow, "catalog", false, catalog).Model.Enabled {
		t.Fatal("explicit allowlist model disabled")
	}
	if got := Resolve(allow, "fetched", true, catalog).Source; got != "fetched" {
		t.Fatalf("source = %q", got)
	}
	if got := Resolve(allow, "custom", false, catalog).Source; got != "custom" {
		t.Fatalf("source = %q", got)
	}
}

func ptr[T any](v T) *T { return &v }
