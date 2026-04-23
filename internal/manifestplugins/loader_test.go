package manifestplugins

import (
	"testing"
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

func TestMerge_UserDisablesBuiltin(t *testing.T) {
	builtinRaw, err := parseRawYAML(builtinYAML)
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
	builtinRaw, err := parseRawYAML(builtinYAML)
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
				{Name: "custom", Repo: "owner/custom", AssetTemplates: map[string]ManifestAsset{
					"linux-amd64": {File: "custom.tar.gz"},
				}},
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
	builtinRaw, err := parseRawYAML(builtinYAML)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm builtin has tool/mise enabled.
	var builtinMiseEnabled *bool
	for _, p := range builtinRaw.Plugins {
		if p.ID == "tool/mise" {
			builtinMiseEnabled = p.Enabled
			break
		}
	}
	if builtinMiseEnabled == nil || !*builtinMiseEnabled {
		t.Fatal("prerequisite: tool/mise must be enabled in builtin")
	}

	// User overrides tool/mise but omits Enabled (nil pointer).
	userRaw := rawManifest{Plugins: []rawManifestPlugin{
		{ID: "tool/mise", Name: "mise-override"},
	}}

	merged := MergeRaw(builtinRaw, userRaw)
	for _, p := range merged.Plugins {
		if p.ID == "tool/mise" {
			if !p.Enabled {
				t.Error("expected tool/mise to inherit enabled:true from builtin when user omits enabled")
			}
			return
		}
	}
	t.Error("tool/mise not found in merged result")
}
