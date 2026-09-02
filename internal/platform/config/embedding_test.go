package config

import (
	"context"
	"testing"
)

type fakeSettingStore struct{ m map[string]string }

func (f *fakeSettingStore) GetSetting(_ context.Context, k string) (string, error) {
	return f.m[k], nil
}

func (f *fakeSettingStore) SetSetting(_ context.Context, k, v string) error {
	f.m[k] = v
	return nil
}

func TestLoadEmbeddingSettings_DefaultsWhenUnset(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	got, err := LoadEmbeddingSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Enabled {
		t.Error("unset deployment must default to disabled")
	}
	if got.Dim != DefaultEmbeddingDim {
		t.Errorf("default dim not applied: %+v", got)
	}
}

func TestEmbeddingSettings_RoundTrip(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	want := EmbeddingSettings{Enabled: true, Dim: 512, Normalize: true}
	if err := SaveEmbeddingSettings(context.Background(), st, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadEmbeddingSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// A zero stored dim falls back to the default rather than producing an unusable
// space key.
func TestLoadEmbeddingSettings_BackfillsBlankDim(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{EmbeddingSettingKey: `{"enabled":true}`}}
	got, err := LoadEmbeddingSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Dim != DefaultEmbeddingDim {
		t.Errorf("blank dim not backfilled: %+v", got)
	}
}

// A model row that survived from before the unification is ignored: the lane's
// model comes from DefaultModels only, so an orphan key must not resurrect it.
func TestLoadEmbeddingSettings_IgnoresLegacyKeys(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{
		EmbeddingSettingKey: `{"enabled":true,"model":"legacy","api_key":"sk-legacy","base_url":"https://legacy"}`,
	}}
	got, err := LoadEmbeddingSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != (EmbeddingSettings{Enabled: true, Dim: DefaultEmbeddingDim}) {
		t.Errorf("got %+v, want only the lane knobs", got)
	}
}

func TestResolveEmbedding_UsesTheProviderBehindTheModelRef(t *testing.T) {
	st := newModelStore(map[string]string{
		EmbeddingSettingKey:     `{"enabled":true,"dim":512,"normalize":true}`,
		DefaultModelsSettingKey: `{"model_embedding":"prov-1/text-embedding-3-small"}`,
	}, Provider{ID: "prov-1", Type: "openai", APIKey: "sk-1", BaseURL: "https://one"})

	got, err := ResolveEmbedding(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := EmbeddingRuntime{
		Enabled: true, Provider: "prov-1", Model: "text-embedding-3-small",
		Dim: 512, APIKey: "sk-1", BaseURL: "https://one", Normalize: true,
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// The type alias is the same one the agent tiers resolve through, and it must
// still report the canonical row id: that id is half the vector-space identity.
func TestResolveEmbedding_TypeAliasReportsCanonicalProvider(t *testing.T) {
	st := newModelStore(map[string]string{
		EmbeddingSettingKey:     `{"enabled":true}`,
		DefaultModelsSettingKey: `{"model_embedding":"openai/text-embedding-3-small"}`,
	}, Provider{ID: "prov-1", Type: "openai", APIKey: "sk-1"})

	got, err := ResolveEmbedding(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Provider != "prov-1" || got.APIKey != "sk-1" {
		t.Errorf("got %+v, want the canonical row behind the type alias", got)
	}
}

// An enabled lane whose model does not resolve degrades to keyword search rather
// than claiming to run: the stored flag is intent, Enabled is the effective state.
func TestResolveEmbedding_UnresolvableRefComesBackDisabled(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"no model named": {
			EmbeddingSettingKey: `{"enabled":true}`,
		},
		"provider row gone": {
			EmbeddingSettingKey:     `{"enabled":true}`,
			DefaultModelsSettingKey: `{"model_embedding":"deleted/text-embedding-3-small"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveEmbedding(context.Background(), newModelStore(settings))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.Enabled || got.APIKey != "" {
				t.Errorf("got %+v, want a disabled lane with no credentials", got)
			}
		})
	}
}
