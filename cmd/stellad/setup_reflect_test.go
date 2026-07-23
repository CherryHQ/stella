package main

import (
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/reflect"
)

func TestResolveUsageCuratorSettingsDefaultArmed(t *testing.T) {
	// Empty and whitespace-only both mean "unset" => armed (the raw string is
	// trimmed here, since the config layer carries it verbatim).
	for _, raw := range []string{"", "   "} {
		settings, err := resolveUsageCuratorSettings(raw)
		if err != nil {
			t.Fatalf("resolveUsageCuratorSettings(%q): %v", raw, err)
		}
		if settings.Mode != reflect.UsageCuratorModeArmed {
			t.Fatalf("mode = %q, want armed", settings.Mode)
		}
	}
}

func TestValidateReflectModeCompatibility(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantErr  bool
		wantText string
	}{
		{name: "empty"},
		{name: "whitespace", raw: "   "},
		{name: "structured case insensitive", raw: " Structured "},
		{name: "stale legacy", raw: "legacy", wantErr: true, wantText: "no longer supported"},
		{name: "invalid", raw: "both", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReflectModeCompatibility(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateReflectModeCompatibility(%q) error = %v, wantErr %t", tt.raw, err, tt.wantErr)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("validateReflectModeCompatibility(%q) error = %q, want text %q", tt.raw, err, tt.wantText)
			}
		})
	}
}

func TestResolveUsageCuratorSettingsShadow(t *testing.T) {
	settings, err := resolveUsageCuratorSettings("shadow")
	if err != nil {
		t.Fatalf("resolveUsageCuratorSettings: %v", err)
	}
	if settings.Mode != reflect.UsageCuratorModeShadow {
		t.Fatalf("mode = %q, want shadow", settings.Mode)
	}
}

func TestResolveUsageCuratorSettingsArmed(t *testing.T) {
	settings, err := resolveUsageCuratorSettings("armed")
	if err != nil {
		t.Fatalf("resolveUsageCuratorSettings: %v", err)
	}
	if settings.Mode != reflect.UsageCuratorModeArmed {
		t.Fatalf("mode = %q, want armed", settings.Mode)
	}
}

func TestResolveUsageCuratorSettingsRejectsInvalidMode(t *testing.T) {
	if _, err := resolveUsageCuratorSettings("enabled"); err == nil {
		t.Fatal("expected invalid curator mode to fail")
	}
}

// TestResolveReflectInterval covers the lenient interval behavior: default on
// empty, warn-and-default on garbage, clamp-up below the minimum, and honoring a
// valid override.
func TestResolveReflectInterval(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", defaultReflectInterval},
		{"garbage", defaultReflectInterval},
		{"1s", minReflectInterval},
		{"2h", 2 * time.Hour},
	}
	for _, tc := range cases {
		if got := resolveReflectInterval(tc.raw); got != tc.want {
			t.Errorf("resolveReflectInterval(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
