package manifestplugins

import (
	"testing"

	"github.com/CherryHQ/stella/resources"
)

func TestLoadBuiltin(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	if len(m.Plugins) == 0 {
		t.Fatal("LoadBuiltin() returned empty plugins")
	}
}

func TestLoadBuiltinLarkCLIOAuthProvider(t *testing.T) {
	m, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("LoadBuiltin() error: %v", err)
	}
	for _, p := range m.Plugins {
		if p.ID != "tool/lark-cli" {
			continue
		}
		if p.OAuthProvider != "lark" {
			t.Fatalf("OAuthProvider = %q, want lark", p.OAuthProvider)
		}
		for _, se := range p.SessionEnvs {
			if se.EnvVar == "LARKSUITE_CLI_BRAND" && se.Source != "oauth.brand" {
				t.Fatalf("LARKSUITE_CLI_BRAND source = %q, want oauth.brand", se.Source)
			}
		}
		return
	}
	t.Fatal("tool/lark-cli not found")
}

func TestMerge_UserDisablesBuiltin(t *testing.T) {
	builtinRaw, err := parseRawYAML(resources.BuiltinPluginsYAML())
	if err != nil {
		t.Fatal(err)
	}

	f := false
	userRaw := rawManifest{Plugins: []rawManifestPlugin{
		{ID: "tool/tap-web", Enabled: &f},
	}}

	merged := MergeRaw(builtinRaw, userRaw)
	for _, p := range merged.Plugins {
		if p.ID == "tool/tap-web" {
			if p.Enabled {
				t.Error("expected tool/tap-web to be disabled after user override")
			}
			return
		}
	}
	t.Error("tool/tap-web not found in merged result")
}

func TestMerge_UserAddsNewPlugin(t *testing.T) {
	builtinRaw, err := parseRawYAML(resources.BuiltinPluginsYAML())
	if err != nil {
		t.Fatal(err)
	}

	tr := true
	userRaw := rawManifest{Plugins: []rawManifestPlugin{
		{
			ID:      "tool/custom",
			Kind:    "tool",
			Name:    "custom",
			Enabled: &tr,
			Binaries: []ManifestBinary{
				{Name: "custom", Tool: "github:owner/custom"},
			},
		},
	}}

	merged := MergeRaw(builtinRaw, userRaw)
	for _, p := range merged.Plugins {
		if p.ID == "tool/custom" {
			return
		}
	}
	t.Error("expected tool/custom to appear in merged result")
}

func TestMerge_EmptyUser(t *testing.T) {
	builtin, err := LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}

	merged := Merge(builtin, &Manifest{})
	if len(merged.Plugins) != len(builtin.Plugins) {
		t.Errorf("expected %d plugins, got %d", len(builtin.Plugins), len(merged.Plugins))
	}
}

func TestMerge_UserOmitsEnabled_InheritsBuiltin(t *testing.T) {
	builtinRaw, err := parseRawYAML(resources.BuiltinPluginsYAML())
	if err != nil {
		t.Fatal(err)
	}

	// Confirm builtin has tool/tap-web enabled.
	var builtinTapEnabled *bool
	for _, p := range builtinRaw.Plugins {
		if p.ID == "tool/tap-web" {
			builtinTapEnabled = p.Enabled
			break
		}
	}
	if builtinTapEnabled == nil || !*builtinTapEnabled {
		t.Fatal("prerequisite: tool/tap-web must be enabled in builtin")
	}

	// User overrides tool/tap-web but omits Enabled (nil pointer).
	userRaw := rawManifest{Plugins: []rawManifestPlugin{
		{ID: "tool/tap-web", Name: "tap-web-override"},
	}}

	merged := MergeRaw(builtinRaw, userRaw)
	for _, p := range merged.Plugins {
		if p.ID == "tool/tap-web" {
			if !p.Enabled {
				t.Error("expected tool/tap-web to inherit enabled:true from builtin when user omits enabled")
			}
			return
		}
	}
	t.Error("tool/tap-web not found in merged result")
}
