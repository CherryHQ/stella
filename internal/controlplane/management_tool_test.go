package controlplane

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
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

type originProviderStore struct {
	config.Store
	snapshot config.ProviderSnapshot
	updated  bool
}

func (s *originProviderStore) GetProviderSnapshot(context.Context, string) (config.ProviderSnapshot, error) {
	return s.snapshot, nil
}

func (s *originProviderStore) ListProviderSnapshots(context.Context) ([]config.ProviderSnapshot, error) {
	return []config.ProviderSnapshot{s.snapshot}, nil
}

func (s *originProviderStore) UpdateProviderIfVersion(context.Context, config.Provider, string) (bool, error) {
	s.updated = true
	return true, nil
}

func (*originProviderStore) DeleteProviderIfVersion(context.Context, string, string) (bool, error) {
	return false, nil
}

func (h *rejectingProviderHandler) Create(context.Context, SettingsProviderCreateInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) Delete(context.Context, SettingsProviderDeleteInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) Get(context.Context, SettingsProviderGetInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) List(context.Context, SettingsProviderListInput) (any, error) {
	h.called = true
	return nil, nil
}

func (h *rejectingProviderHandler) Update(context.Context, SettingsProviderUpdateInput) (any, error) {
	h.called = true
	return nil, nil
}

func TestSettingsProviderDispatchRejectsNestedSecretShapedFields(t *testing.T) {
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
				_, err := SettingsProviderDispatch(t.Context(), h, action, args)
				if err == nil || !strings.Contains(err.Error(), `unknown field "`+tc.field+`"`) {
					t.Fatalf("SettingsProviderDispatch error = %v, want nested %q rejection", err, tc.field)
				}
				if h.called {
					t.Fatal("handler received an input with a forbidden nested field")
				}
			})
		}
	}
}

func TestSettingsProviderDispatchRejectsObjectModelModalities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		models map[string]any
	}{
		{
			name:   "input",
			models: map[string]any{"model": map[string]any{"enabled": true, "input": []any{map[string]any{"api_key": "canary-input-secret"}}}},
		},
		{
			name:   "output",
			models: map[string]any{"model": map[string]any{"enabled": true, "output": []any{map[string]any{"credential_ref": "canary-output-secret"}}}},
		},
	} {
		for _, action := range []string{"create", "update"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				args := map[string]any{
					"id": "provider", "type": "openai", "name": "Provider", "enabled": true,
					"base_url": "https://provider.example.test", "models": tc.models,
				}
				if action == "update" {
					args["expected_version"] = "version"
				}
				h := &rejectingProviderHandler{}
				if _, err := SettingsProviderDispatch(t.Context(), h, action, args); err == nil {
					t.Fatal("SettingsProviderDispatch accepted an object where a model modality must be a string")
				}
				if h.called {
					t.Fatal("handler received a model modality object")
				}
			})
		}
	}
}

func TestProviderToolSchemasSealNestedModels(t *testing.T) {
	for _, spec := range SettingsProviderActionTools() {
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

func TestProviderToolUpdateRejectsOriginChangeWithOnlyAgentCredentialOverride(t *testing.T) {
	store := &originProviderStore{snapshot: config.ProviderSnapshot{
		// The global key is deliberately empty: an Agent-specific encrypted
		// override can still authenticate requests through this Provider.
		Provider: config.Provider{ID: "provider", Type: "openai", Name: "Provider", Enabled: true, BaseURL: "https://api.example.test/v1"},
		Version:  "version",
	}}
	access, err := NewService(store, nil, nil, nil, nil, nil).Begin(t.Context(), adminAuthority(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = (providerManagementHandler{access: access}).Update(t.Context(), SettingsProviderUpdateInput{
		Id: "provider", Type: "openai", Name: "Provider", Enabled: true,
		BaseUrl: "https://attacker.example.test/v1", ExpectedVersion: "version",
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("origin change with global-empty key = %v, want ConflictError", err)
	}
	if store.updated {
		t.Fatal("origin change reached the provider write")
	}
}

func TestProviderToolUpdatePreservesOmittedModels(t *testing.T) {
	current := config.Provider{ID: "provider", CatalogID: "deepseek", ModelPolicy: "allowlist", Models: map[string]config.ProviderModelOverride{"kept": {Enabled: config.ValuePtr(true)}}}
	candidate, err := providerFromInput(current.ID, SettingsProviderCreateInput{Id: current.ID, Type: "openai", Name: "provider", Enabled: true, BaseUrl: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Models != nil {
		t.Fatalf("candidate models = %#v, want omitted", candidate.Models)
	}
	candidate.CatalogID = current.CatalogID
	candidate.ModelPolicy = current.ModelPolicy
	if candidate.CatalogID != "deepseek" || candidate.ModelPolicy != "allowlist" {
		t.Fatalf("deployment fields lost: %#v", candidate)
	}
	// The tool handler preserves the stored catalog when generated input omitted
	// models, rather than turning an omitted field into an accidental clear.
	candidate.Models = current.Models
	if _, ok := candidate.Models["kept"]; !ok {
		t.Fatalf("omitted models were not preserved: %#v", candidate.Models)
	}
}
