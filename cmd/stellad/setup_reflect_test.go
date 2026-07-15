package main

import (
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/reflect"
)

func TestResolveUsageCuratorSettingsDefaultShadow(t *testing.T) {
	// Empty and whitespace-only both mean "unset" => shadow (the raw string is
	// trimmed here, since the config layer carries it verbatim).
	for _, raw := range []string{"", "   "} {
		settings, err := resolveUsageCuratorSettings(raw)
		if err != nil {
			t.Fatalf("resolveUsageCuratorSettings(%q): %v", raw, err)
		}
		if settings.Mode != reflect.UsageCuratorModeShadow {
			t.Fatalf("mode = %q, want shadow", settings.Mode)
		}
	}
}

func TestResolveReflectMode(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    reflect.RuntimeMode
		wantErr bool
	}{
		{name: "empty defaults to legacy", want: reflect.RuntimeModeLegacy},
		{name: "whitespace defaults to legacy", raw: "   ", want: reflect.RuntimeModeLegacy},
		{name: "legacy", raw: "legacy", want: reflect.RuntimeModeLegacy},
		{name: "structured case insensitive", raw: " Structured ", want: reflect.RuntimeModeStructured},
		{name: "invalid", raw: "both", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveReflectMode(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveReflectMode(%q) error = %v, wantErr %t", tt.raw, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("resolveReflectMode(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
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
