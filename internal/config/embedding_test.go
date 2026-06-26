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
	if got.Model != DefaultEmbeddingModel || got.Dim != DefaultEmbeddingDim {
		t.Errorf("defaults not applied: %+v", got)
	}
	if got.Provider != EmbeddingProviderAPI {
		t.Errorf("unset provider must default to api (backward compat), got %q", got.Provider)
	}
}

func TestEmbeddingSettings_RoundTrip(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	want := EmbeddingSettings{Enabled: true, Provider: EmbeddingProviderLocal, Model: "m", Dim: 512, APIKey: "sk-x", BaseURL: "https://x", Normalize: true}
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

// A blank/zero stored model or dim falls back to defaults rather than producing
// an unusable empty space key.
func TestLoadEmbeddingSettings_BackfillsBlankModelAndDim(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{EmbeddingSettingKey: `{"enabled":true,"api_key":"k"}`}}
	got, err := LoadEmbeddingSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Model != DefaultEmbeddingModel || got.Dim != DefaultEmbeddingDim {
		t.Errorf("blank model/dim not backfilled: %+v", got)
	}
}
