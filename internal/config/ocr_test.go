package config

import (
	"context"
	"testing"
)

func TestLoadOCRSettings_DefaultsDisabled(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	got, err := LoadOCRSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Enabled {
		t.Error("unset deployment must default to OCR disabled")
	}
}

func TestOCRSettings_RoundTrip(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	if err := SaveOCRSettings(context.Background(), st, OCRSettings{Enabled: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadOCRSettings(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Enabled {
		t.Errorf("round-trip lost enabled: %+v", got)
	}
}
