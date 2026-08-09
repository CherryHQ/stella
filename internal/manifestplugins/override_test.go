package manifestplugins

import (
	"encoding/json"
	"testing"
)

func builtinPlugin() ManifestPlugin {
	return ManifestPlugin{
		ID:          "tool/gh",
		Kind:        "tool",
		Name:        "gh",
		DisplayName: "GitHub CLI",
		Description: "the description that shipped",
		Enabled:     true,
		Prompt:      "use gh for GitHub",
		Binaries:    []ManifestBinary{{Name: "gh", Tool: "github:cli/cli", Version: "latest"}},
	}
}

// The whole point of the sparse override: edit one field, and the next release's
// improvements to every other field still arrive.
func TestUneditedFieldsKeepFollowingTheShippedDefinition(t *testing.T) {
	base := builtinPlugin()
	edited := base
	edited.Binaries = []ManifestBinary{{Name: "gh", Tool: "npm:gh", Version: "2.0.0"}}

	override, err := OverrideJSON(base, edited)
	if err != nil {
		t.Fatal(err)
	}

	// A later release rewrites the description and the prompt.
	next := builtinPlugin()
	next.Description = "a much better description"
	next.Prompt = "a much better prompt"

	got, err := ApplyOverride(next, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != next.Description || got.Prompt != next.Prompt {
		t.Errorf("upgrade was swallowed: description=%q prompt=%q", got.Description, got.Prompt)
	}
	if len(got.Binaries) != 1 || got.Binaries[0].Tool != "npm:gh" {
		t.Errorf("the edited field did not survive: %#v", got.Binaries)
	}
}

// An unchanged plugin needs no row at all — that is what lets a save drop the
// override instead of pinning the definition by accident.
func TestOverrideOfAnUnchangedPluginIsEmpty(t *testing.T) {
	base := builtinPlugin()
	override, err := OverrideJSON(base, base)
	if err != nil {
		t.Fatal(err)
	}
	if override != "" {
		t.Errorf("override = %q, want empty", override)
	}
}

// Only the edited field is stored. If more keys land in the row, the untouched
// ones are being pinned again and the bug is back.
func TestOverrideStoresOnlyTheEditedField(t *testing.T) {
	base := builtinPlugin()
	edited := base
	edited.DisplayName = "gh (ours)"

	override, err := OverrideJSON(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(override), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 {
		t.Fatalf("override = %s, want display_name alone", override)
	}
	if _, ok := fields["display_name"]; !ok {
		t.Fatalf("override = %s, want display_name", override)
	}
}

// Absent means inherit, so emptying an optional field has to say so explicitly.
func TestClearingAnOptionalFieldIsRecordedAndApplied(t *testing.T) {
	base := builtinPlugin()
	edited := base
	edited.Prompt = ""

	override, err := OverrideJSON(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(override), &fields); err != nil {
		t.Fatal(err)
	}
	if v, ok := fields["prompt"]; !ok || v != nil {
		t.Fatalf("override = %s, want an explicit null prompt", override)
	}
	got, err := ApplyOverride(base, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "" {
		t.Errorf("prompt = %q, want it cleared", got.Prompt)
	}
}

// ID, Enabled and Builtin are the caller's: the override neither carries nor
// disturbs them.
func TestApplyOverrideLeavesIdentityAndTheEnableSwitchAlone(t *testing.T) {
	base := builtinPlugin()
	base.Enabled = false
	base.Builtin = true
	edited := builtinPlugin()
	edited.DisplayName = "renamed"

	override, err := OverrideJSON(builtinPlugin(), edited)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyOverride(base, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != base.ID || got.Enabled || !got.Builtin {
		t.Errorf("identity drifted: id=%q enabled=%v builtin=%v", got.ID, got.Enabled, got.Builtin)
	}
	if got.DisplayName != "renamed" {
		t.Errorf("display_name = %q", got.DisplayName)
	}
}

// Rows written before overrides went sparse hold a whole definition. They must
// keep resolving to the same plugin.
func TestAPreSparseFullSnapshotStillResolves(t *testing.T) {
	base := builtinPlugin()
	snapshot, err := DefinitionJSON(ManifestPlugin{
		Kind:        "tool",
		Name:        "gh",
		DisplayName: "GitHub CLI",
		Description: "pinned by an old row",
		Prompt:      "use gh for GitHub",
		Binaries:    []ManifestBinary{{Name: "gh", Tool: "npm:gh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyOverride(base, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "pinned by an old row" || got.Binaries[0].Tool != "npm:gh" {
		t.Errorf("old row lost its customization: %#v", got)
	}
	if got.ID != base.ID {
		t.Errorf("id = %q", got.ID)
	}
}

func TestApplyOverrideRejectsCorruptJSON(t *testing.T) {
	if _, err := ApplyOverride(builtinPlugin(), "{not json"); err == nil {
		t.Fatal("want an error for a corrupt override")
	}
}
