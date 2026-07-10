package main

import (
	"testing"

	"github.com/CherryHQ/stella/internal/reflect"
)

func TestResolveUsageCuratorSettingsDefaultShadow(t *testing.T) {
	t.Setenv("STELLA_REFLECT_CURATOR_MODE", "")

	settings, err := resolveUsageCuratorSettings()
	if err != nil {
		t.Fatalf("resolveUsageCuratorSettings: %v", err)
	}
	if settings.Mode != reflect.UsageCuratorModeShadow {
		t.Fatalf("mode = %q, want shadow", settings.Mode)
	}
}

func TestResolveUsageCuratorSettingsArmed(t *testing.T) {
	t.Setenv("STELLA_REFLECT_CURATOR_MODE", "armed")

	settings, err := resolveUsageCuratorSettings()
	if err != nil {
		t.Fatalf("resolveUsageCuratorSettings: %v", err)
	}
	if settings.Mode != reflect.UsageCuratorModeArmed {
		t.Fatalf("mode = %q, want armed", settings.Mode)
	}
}

func TestResolveUsageCuratorSettingsRejectsInvalidMode(t *testing.T) {
	t.Setenv("STELLA_REFLECT_CURATOR_MODE", "enabled")

	if _, err := resolveUsageCuratorSettings(); err == nil {
		t.Fatal("expected invalid curator mode to fail")
	}
}
