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
