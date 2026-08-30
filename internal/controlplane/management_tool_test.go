package controlplane

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

func TestProviderToolProjectionRedactsLegacyEndpoint(t *testing.T) {
	view := projectProvider(config.Provider{
		ID: "legacy", Type: "openai", Name: "legacy", Enabled: true,
		BaseURL: "https://user:token@example.test/v1?api_key=leaked#fragment",
	}, "version")
	if view.BaseURL != "" || !view.EndpointRedacted {
		t.Fatalf("legacy endpoint projection = %#v, want redacted", view)
	}
}

func TestProviderToolUpdatePreservesOmittedModels(t *testing.T) {
	current := config.Provider{ID: "provider", Models: map[string]config.ProviderModel{"kept": {ID: "kept", Enabled: true}}}
	candidate, err := providerFromInput(current.ID, ProviderCreateInput{Id: current.ID, Type: "openai", Name: "provider", Enabled: true, BaseUrl: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Models != nil {
		t.Fatalf("candidate models = %#v, want omitted", candidate.Models)
	}
	// The tool handler preserves the stored catalog when generated input omitted
	// models, rather than turning an omitted field into an accidental clear.
	candidate.Models = current.Models
	if _, ok := candidate.Models["kept"]; !ok {
		t.Fatalf("omitted models were not preserved: %#v", candidate.Models)
	}
}
