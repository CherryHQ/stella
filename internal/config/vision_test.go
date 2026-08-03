package config

import (
	"context"
	"testing"
)

func TestLoadVisionSettings_UnsetMeansNoModel(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	got, err := LoadVisionSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Model != "" {
		t.Errorf("Model = %q, want empty so image understanding degrades to extraction", got.Model)
	}
}

func TestVisionSettings_RoundTrip(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	want := VisionSettings{Model: "openai/gpt-4o"}
	if err := SaveVisionSettings(context.Background(), st, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadVisionSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

// A padded model ref must not survive: it would be compared against provider IDs
// and resolve to nothing, disabling vision with no visible cause.
func TestVisionSettings_TrimsModel(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{VisionSettingKey: `{"model":"  openai/gpt-4o \n"}`}}
	got, err := LoadVisionSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Model != "openai/gpt-4o" {
		t.Errorf("Model = %q, want the trimmed ref", got.Model)
	}
}

func TestLoadVisionSettings_MalformedJSONIsAnError(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{VisionSettingKey: `{"model":`}}
	if _, err := LoadVisionSettings(context.Background(), st); err == nil {
		t.Fatal("expected an error for a corrupt setting row")
	}
}
