package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestProviderConfigRoundTripsLegacyCostAndNewFields(t *testing.T) {
	raw := json.RawMessage(`{"api_key":"sk-old","base_url":"https://example.test/v1","catalog_id":"deepseek","model_policy":"allowlist","models":{"deepseek-v3":{"name":"DeepSeek V3","enabled":false,"cost":{"input":0.14,"output":0.28,"cacheRead":0.028,"cacheWrite":0}}}}`)
	provider := providerFromDB(sqlc.Provider{ID: "p", Type: "openai", Name: "P", Enabled: true, Config: raw, UpdatedAt: time.Now().UTC()})
	if provider.CatalogID != "deepseek" || provider.ModelPolicy != "allowlist" {
		t.Fatalf("provider fields = %#v", provider)
	}
	override, ok := provider.Models["deepseek-v3"]
	if !ok || override.Enabled == nil || *override.Enabled {
		t.Fatalf("enabled presence = %#v", override.Enabled)
	}
	if override.Cost == nil || override.Cost.Input == nil || *override.Cost.Input != 0.14 || override.Cost.CacheWrite == nil || *override.Cost.CacheWrite != 0 {
		t.Fatalf("legacy cost = %#v", override.Cost)
	}

	encoded, err := json.Marshal(providerConfig(provider))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["catalog_id"] != "deepseek" || got["model_policy"] != "allowlist" {
		t.Fatalf("encoded provider fields = %#v", got)
	}
	models, ok := got["models"].(map[string]any)
	if !ok || models["deepseek-v3"] == nil {
		t.Fatalf("encoded models = %#v", got["models"])
	}
}
