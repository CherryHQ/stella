package controlplane

import (
	"context"
	"maps"
	"strings"
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

type rejectingProviderHandler struct{ called bool }

func (h *rejectingProviderHandler) Create(context.Context, ProviderCreateInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) Delete(context.Context, ProviderDeleteInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) Get(context.Context, ProviderGetInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) List(context.Context, ProviderListInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) Update(context.Context, ProviderUpdateInput) (any, error) {
	h.called = true
	return nil, nil
}

func TestProviderDispatchRejectsNestedSecretShapedFields(t *testing.T) {
	base := func(models map[string]any) map[string]any {
		return map[string]any{
			"id": "provider", "type": "openai", "name": "Provider", "enabled": true,
			"base_url": "https://provider.example.test", "models": models,
		}
	}
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{
			name:  "model credential",
			args:  base(map[string]any{"model": map[string]any{"enabled": true, "api_key": "canary-model-secret"}}),
			field: "api_key",
		},
		{
			name:  "cost credential",
			args:  base(map[string]any{"model": map[string]any{"enabled": true, "cost": map[string]any{"token": "canary-cost-secret"}}}),
			field: "token",
		},
		{
			name:  "model credential reference",
			args:  base(map[string]any{"model": map[string]any{"enabled": true, "credential_ref": "canary-credential-ref"}}),
			field: "credential_ref",
		},
		{
			name:  "unexpected model field",
			args:  base(map[string]any{"model": map[string]any{"enabled": true, "unexpected": true}}),
			field: "unexpected",
		},
	} {
		for _, action := range []string{"create", "update"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				args := make(map[string]any, len(tc.args)+1)
				maps.Copy(args, tc.args)
				if action == "update" {
					args["expected_version"] = "version"
				}
				h := &rejectingProviderHandler{}
				_, err := ProviderDispatch(t.Context(), h, action, args)
				if err == nil || !strings.Contains(err.Error(), `unknown field "`+tc.field+`"`) {
					t.Fatalf("ProviderDispatch error = %v, want nested %q rejection", err, tc.field)
				}
				if h.called {
					t.Fatal("handler received an input with a forbidden nested field")
				}
			})
		}
	}
}

func TestProviderToolSchemasSealNestedModels(t *testing.T) {
	for _, spec := range ProviderActionTools() {
		if spec.Action != "create" && spec.Action != "update" {
			continue
		}
		schema := spec.InputSchema()
		models := schema["properties"].(map[string]any)["models"].(map[string]any)
		model := models["additionalProperties"].(map[string]any)
		if model["additionalProperties"] != false {
			t.Fatalf("%s model schema is open: %#v", spec.Name, model)
		}
		cost := model["properties"].(map[string]any)["cost"].(map[string]any)
		if cost["additionalProperties"] != false {
			t.Fatalf("%s model cost schema is open: %#v", spec.Name, cost)
		}
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
