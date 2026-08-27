package controlplane

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

// modelFakeStore implements the setting accessors and the provider catalog the
// default-model and embedding paths touch; the rest of config.Store is embedded
// nil (unused here).
type modelFakeStore struct {
	config.Store
	m         map[string]string
	providers []config.Provider
}

func (f *modelFakeStore) GetSetting(_ context.Context, k string) (string, error) {
	return f.m[k], nil
}

func (f *modelFakeStore) SetSetting(_ context.Context, k, v string) error {
	f.m[k] = v
	return nil
}

func (f *modelFakeStore) ListProviders(_ context.Context) ([]config.Provider, error) {
	return f.providers, nil
}

func modelAccess(t *testing.T, store *modelFakeStore) *Access {
	t.Helper()
	acc, err := NewService(store, nil, nil, nil, nil).Begin(context.Background(), adminAuthority(t))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return acc
}

func TestSetDefaultModelsRoundTrip(t *testing.T) {
	store := &modelFakeStore{m: map[string]string{}}
	acc := modelAccess(t, store)

	if _, err := acc.SetDefaultModels(context.Background(), config.DefaultModels{
		Model:          " openai/gpt-5 ",
		ModelThinking:  "high",
		ModelVision:    "openai/gpt-4o",
		ModelEmbedding: "openai/text-embedding-3-small",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := acc.GetDefaultModels(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want the trimmed ref", got.Model)
	}
	if got.ModelVision != "openai/gpt-4o" || got.ModelEmbedding != "openai/text-embedding-3-small" {
		t.Errorf("auxiliary roles lost: %+v", got)
	}
}

// A bare model name has no provider to resolve against at the deployment level,
// so it must be rejected at write time rather than resolving differently per
// agent — or silently to nothing.
func TestSetDefaultModelsRejectsRefWithoutProvider(t *testing.T) {
	for _, ref := range []string{"gpt-4o", "openai/", "/gpt-4o"} {
		store := &modelFakeStore{m: map[string]string{}}
		acc := modelAccess(t, store)
		if _, err := acc.SetDefaultModels(context.Background(), config.DefaultModels{ModelVision: ref}); err == nil {
			t.Errorf("model_vision %q was accepted, want a validation error", ref)
		}
		if len(store.m) != 0 {
			t.Errorf("a rejected model %q must not be persisted", ref)
		}
	}
}

func TestSetDefaultModelsRejectsUnknownThinkingLevel(t *testing.T) {
	store := &modelFakeStore{m: map[string]string{}}
	acc := modelAccess(t, store)
	if _, err := acc.SetDefaultModels(context.Background(), config.DefaultModels{ModelThinking: "extreme"}); err == nil {
		t.Fatal("expected a validation error for an unknown thinking level")
	}
}

// Clearing every role is the supported way to turn the deployment defaults off,
// so empty strings must not trip the provider-prefix check.
func TestSetDefaultModelsAllowsClearing(t *testing.T) {
	store := &modelFakeStore{m: map[string]string{
		config.DefaultModelsSettingKey: `{"model":"openai/gpt-5","model_vision":"openai/gpt-4o"}`,
	}}
	acc := modelAccess(t, store)

	got, err := acc.SetDefaultModels(context.Background(), config.DefaultModels{})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got != (config.DefaultModels{}) {
		t.Errorf("got %+v, want every role cleared", got)
	}
}

// Enabling a lane that cannot embed a single document is a silent no-op, so the
// write is refused and the stored settings are left as they were.
func TestSetEmbeddingSettingsRejectsEnablingWithoutResolvableCredentials(t *testing.T) {
	store := &modelFakeStore{m: map[string]string{}}
	acc := modelAccess(t, store)

	if _, err := acc.SetEmbeddingSettings(context.Background(), EmbeddingUpdate{Enabled: true}); err == nil {
		t.Fatal("expected a validation error with no embedding model configured")
	}
	got, err := acc.GetEmbeddingSettings(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled {
		t.Error("the refused write must not leave the lane enabled")
	}
}

func TestSetEmbeddingSettingsEnablesWithAProviderBackedModel(t *testing.T) {
	store := &modelFakeStore{
		m: map[string]string{
			config.DefaultModelsSettingKey: `{"model_embedding":"prov-1/text-embedding-3-small"}`,
		},
		providers: []config.Provider{{ID: "prov-1", Type: "openai", APIKey: "sk-provider"}},
	}
	acc := modelAccess(t, store)

	got, err := acc.SetEmbeddingSettings(context.Background(), EmbeddingUpdate{Enabled: true, Dim: 512, Normalize: true})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !got.Enabled || got.Dim != 512 || !got.Normalize {
		t.Errorf("got %+v, want the lane knobs persisted", got)
	}
}
