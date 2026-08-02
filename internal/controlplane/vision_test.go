package controlplane

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

// visionFakeStore implements only the two setting accessors the vision settings
// touch; the rest of config.Store is embedded nil (unused here).
type visionFakeStore struct {
	config.Store
	m map[string]string
}

func (f *visionFakeStore) GetSetting(_ context.Context, k string) (string, error) {
	return f.m[k], nil
}

func (f *visionFakeStore) SetSetting(_ context.Context, k, v string) error {
	f.m[k] = v
	return nil
}

func visionAccess(t *testing.T, store *visionFakeStore) *Access {
	t.Helper()
	acc, err := NewService(store, nil, nil, nil, nil).Begin(context.Background(), adminAuthority(t))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return acc
}

func TestSetVisionSettingsRoundTrip(t *testing.T) {
	store := &visionFakeStore{m: map[string]string{}}
	acc := visionAccess(t, store)

	if _, err := acc.SetVisionSettings(context.Background(), config.VisionSettings{Model: " openai/gpt-4o "}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := acc.GetVisionSettings(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Model != "openai/gpt-4o" {
		t.Errorf("Model = %q, want the trimmed ref", got.Model)
	}
}

// A bare model name has no provider to resolve against at the deployment level,
// so it must be rejected at write time rather than resolving differently per
// agent — or silently to nothing.
func TestSetVisionSettingsRejectsRefWithoutProvider(t *testing.T) {
	store := &visionFakeStore{m: map[string]string{}}
	acc := visionAccess(t, store)

	for _, ref := range []string{"gpt-4o", "openai/", "/gpt-4o"} {
		if _, err := acc.SetVisionSettings(context.Background(), config.VisionSettings{Model: ref}); err == nil {
			t.Errorf("model %q was accepted, want a validation error", ref)
		}
	}
	if len(store.m) != 0 {
		t.Error("a rejected model must not be persisted")
	}
}

// Clearing the model is the supported way to turn vision off, so an empty string
// must not trip the provider-prefix check.
func TestSetVisionSettingsAllowsClearing(t *testing.T) {
	store := &visionFakeStore{m: map[string]string{config.VisionSettingKey: `{"model":"openai/gpt-4o"}`}}
	acc := visionAccess(t, store)

	got, err := acc.SetVisionSettings(context.Background(), config.VisionSettings{Model: ""})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got.Model != "" {
		t.Errorf("Model = %q, want empty", got.Model)
	}
}
