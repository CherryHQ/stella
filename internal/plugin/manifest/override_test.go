package manifest

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func builtinPlugin() ManifestPlugin {
	return ManifestPlugin{
		ID:      "tool/gh",
		Kind:    "tool",
		Enabled: true,
		ManifestPluginDefinition: ManifestPluginDefinition{
			Name:        "gh",
			DisplayName: "GitHub CLI",
			Description: "the description that shipped",
			Prompt:      "use gh for GitHub",
			Binaries:    []ManifestBinary{{Name: "gh", Tool: "github:cli/cli", Version: "latest"}},
		},
	}
}

func pinnedBinaries(t *testing.T, override string, tool string) string {
	t.Helper()
	edited := builtinPlugin()
	edited.Binaries = []ManifestBinary{{Name: "gh", Tool: tool}}
	out, err := SetFields(override, edited, []string{"binaries"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The whole point of the sparse override: take one field, and the next release's
// improvements to every other field still arrive.
func TestUnownedFieldsKeepFollowingTheShippedDefinition(t *testing.T) {
	override := pinnedBinaries(t, "", "npm:gh")

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
		t.Errorf("the owned field did not survive: %#v", got.Binaries)
	}
}

// Ownership is recorded, not recomputed. Deriving it from a diff against the
// running builtin let a release that happened to ship the pinned value quietly
// release the pin — and the release after that would move the plugin.
func TestAPinSurvivesAReleaseThatShipsTheSameValue(t *testing.T) {
	override := pinnedBinaries(t, "", "npm:gh@2")

	// A release catches up: the shipped default is now what the admin pinned.
	caughtUp := builtinPlugin()
	caughtUp.Binaries = []ManifestBinary{{Name: "gh", Tool: "npm:gh@2"}}

	// The admin saves something unrelated while that release is running.
	edited, err := ApplyOverride(caughtUp, override)
	if err != nil {
		t.Fatal(err)
	}
	edited.DisplayName = "gh (ours)"
	override, err = SetFields(override, edited, []string{"display_name"})
	if err != nil {
		t.Fatal(err)
	}

	// The release after that moves the default on.
	movedOn := builtinPlugin()
	movedOn.Binaries = []ManifestBinary{{Name: "gh", Tool: "npm:gh@3"}}

	got, err := ApplyOverride(movedOn, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Binaries[0].Tool != "npm:gh@2" {
		t.Errorf("binaries = %q, want the pin to still hold", got.Binaries[0].Tool)
	}
}

// A save says what it changes. Fields nobody named keep the ownership they had,
// which is what makes a one-field edit safe to send.
func TestSaveOnlyTakesTheFieldsItNames(t *testing.T) {
	override := pinnedBinaries(t, "", "npm:gh")

	edited := builtinPlugin()
	edited.DisplayName = "gh (ours)"
	override, err := SetFields(override, edited, []string{"display_name"})
	if err != nil {
		t.Fatal(err)
	}

	owned, err := OwnedFields(override)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owned, []string{"display_name", "binaries"}) {
		t.Fatalf("owned = %v, want display_name and binaries", owned)
	}
}

// An unchanged plugin needs no row at all — that is what lets a reset drop the
// override instead of pinning the definition by accident.
func TestAnOverrideThatOwnsNothingIsEmpty(t *testing.T) {
	override, err := SetFields("", builtinPlugin(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if override != "" {
		t.Errorf("override = %q, want empty", override)
	}
}

// Only the named field is stored. If more keys land in the row, untouched fields
// are being pinned again and the bug is back.
func TestOverrideStoresOnlyTheNamedField(t *testing.T) {
	edited := builtinPlugin()
	edited.DisplayName = "gh (ours)"
	override, err := SetFields("", edited, []string{"display_name"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(override), &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "$sparse")
	if len(fields) != 1 {
		t.Fatalf("override = %s, want display_name alone", override)
	}
	if _, ok := fields["display_name"]; !ok {
		t.Fatalf("override = %s, want display_name", override)
	}
}

// Absent means inherit, so owning a field at its empty value has to say so.
func TestOwningAnEmptyFieldIsRecordedAndApplied(t *testing.T) {
	edited := builtinPlugin()
	edited.Prompt = ""
	override, err := SetFields("", edited, []string{"prompt"})
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
	got, err := ApplyOverride(builtinPlugin(), override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "" {
		t.Errorf("prompt = %q, want it owned as empty", got.Prompt)
	}
}

func TestSetFieldsRejectsAFieldThatIsNotInTheDefinition(t *testing.T) {
	if _, err := SetFields("", builtinPlugin(), []string{"enabled"}); err == nil {
		t.Fatal("want an error: enabled is a column, not a definition field")
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

	override, err := SetFields("", edited, []string{"display_name"})
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

// Releasing one field hands exactly that field back and leaves the others pinned.
func TestReleaseFieldHandsBackOneFieldOnly(t *testing.T) {
	edited := builtinPlugin()
	edited.DisplayName = "gh (ours)"
	edited.Binaries = []ManifestBinary{{Name: "gh", Tool: "npm:gh"}}
	override, err := SetFields("", edited, []string{"display_name", "binaries"})
	if err != nil {
		t.Fatal(err)
	}

	override, err = ReleaseField(override, "binaries")
	if err != nil {
		t.Fatal(err)
	}

	next := builtinPlugin()
	next.Binaries = []ManifestBinary{{Name: "gh", Tool: "github:cli/cli", Version: "9.9.9"}}
	got, err := ApplyOverride(next, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Binaries[0].Version != "9.9.9" {
		t.Errorf("binaries = %#v, want the shipped definition back", got.Binaries)
	}
	if got.DisplayName != "gh (ours)" {
		t.Errorf("display_name = %q, want the other field still owned", got.DisplayName)
	}
}

func TestReleasingAFieldNobodyOwnsIsReported(t *testing.T) {
	edited := builtinPlugin()
	edited.DisplayName = "gh (ours)"
	override, err := SetFields("", edited, []string{"display_name"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseField(override, "prompt"); !errors.Is(err, ErrFieldNotOwned) {
		t.Fatalf("err = %v, want ErrFieldNotOwned", err)
	}
}

// Releasing the last field leaves no row to keep.
func TestReleasingTheLastFieldEmptiesTheOverride(t *testing.T) {
	edited := builtinPlugin()
	edited.DisplayName = "gh (ours)"
	override, err := SetFields("", edited, []string{"display_name"})
	if err != nil {
		t.Fatal(err)
	}
	override, err = ReleaseField(override, "display_name")
	if err != nil {
		t.Fatal(err)
	}
	if override != "" {
		t.Errorf("override = %q, want empty", override)
	}
}

func legacySnapshot(t *testing.T) string {
	t.Helper()
	snapshot, err := DefinitionJSON(ManifestPlugin{
		Kind: "tool",
		ManifestPluginDefinition: ManifestPluginDefinition{
			Name:        "gh",
			DisplayName: "GitHub CLI",
			Description: "pinned by an old row",
			Binaries:    []ManifestBinary{{Name: "gh", Tool: "npm:gh"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// Rows written before overrides went sparse hold a whole definition, and in that
// format an absent key means empty, not inherit. Reading one as a patch would
// hand the admin a later release's prompt they never asked for.
func TestAPreSparseFullSnapshotStillPinsEveryField(t *testing.T) {
	// The builtin underneath has since grown a prompt the old row never had.
	base := builtinPlugin()
	base.Prompt = "a prompt added in a later release"

	got, err := ApplyOverride(base, legacySnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "pinned by an old row" || got.Binaries[0].Tool != "npm:gh" {
		t.Errorf("old row lost its customization: %#v", got)
	}
	if got.Prompt != "" {
		t.Errorf("prompt = %q, want the old row's empty value rather than an inherited one", got.Prompt)
	}
	if got.ID != base.ID {
		t.Errorf("id = %q", got.ID)
	}
}

// A legacy row owns every field, including the ones its JSON omits and therefore
// owns as empty. Saying so is what lets the editor show it and release one.
func TestALegacyRowReportsOwningEverything(t *testing.T) {
	owned, err := OwnedFields(legacySnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owned, OwnableFields()) {
		t.Fatalf("owned = %v, want every definition field", owned)
	}
}

// Converting a legacy row on the way to releasing one field must not disturb the
// others — including the ones it owned as empty.
func TestReleasingOneFieldFromALegacyRowKeepsTheRestPinned(t *testing.T) {
	override, err := ReleaseField(legacySnapshot(t), "prompt")
	if err != nil {
		t.Fatal(err)
	}

	next := builtinPlugin()
	next.Prompt = "a prompt added in a later release"
	next.Description = "a description added in a later release"

	got, err := ApplyOverride(next, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != next.Prompt {
		t.Errorf("prompt = %q, want the released field to follow the server", got.Prompt)
	}
	if got.Description != "pinned by an old row" {
		t.Errorf("description = %q, want it still pinned by the old row", got.Description)
	}
	if got.Binaries[0].Tool != "npm:gh" {
		t.Errorf("binaries = %#v, want them still pinned", got.Binaries)
	}
}

// The marker is what separates the two formats, and a sparse row wears it.
func TestSparseOverrideIsMarked(t *testing.T) {
	edited := builtinPlugin()
	edited.DisplayName = "gh (ours)"
	override, err := SetFields("", edited, []string{"display_name"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(override), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["$sparse"] != true {
		t.Fatalf("override = %s, want the sparse marker", override)
	}
	// And the marker never reaches the plugin.
	got, err := ApplyOverride(builtinPlugin(), override)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "gh (ours)" {
		t.Errorf("display_name = %q", got.DisplayName)
	}
}

func TestApplyOverrideRejectsCorruptJSON(t *testing.T) {
	if _, err := ApplyOverride(builtinPlugin(), "{not json"); err == nil {
		t.Fatal("want an error for a corrupt override")
	}
}

// kind and essential belong to the server, so no override may take them —
// including a legacy row, which was a snapshot of the whole resolved plugin and
// therefore carried the server's own values incidentally, never as a decision
// anyone made. Materializing those would pin a plugin's identity and its
// core-tool policy at whatever they were the day someone renamed it.
func TestServerOwnedFieldsCannotBeTaken(t *testing.T) {
	edited := builtinPlugin()
	edited.Kind = "hook"
	edited.Essential = true
	for _, field := range []string{"kind", "essential"} {
		if _, err := SetFields("", edited, []string{field}); err == nil {
			t.Errorf("SetFields(%q) = nil error, want a refusal", field)
		}
		if _, err := ReleaseField(`{"$sparse":true,"display_name":"x"}`, field); err == nil {
			t.Errorf("ReleaseField(%q) = nil error, want a refusal", field)
		}
		if IsOwnableField(field) {
			t.Errorf("IsOwnableField(%q) = true, want false", field)
		}
		if slices.Contains(OwnableFields(), field) {
			t.Errorf("OwnableFields() contains %q", field)
		}
	}

	legacy, err := DefinitionJSON(edited)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := OwnedFields(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(owned, "kind") || slices.Contains(owned, "essential") {
		t.Fatalf("a legacy row reports owning %v, want kind and essential left to the server", owned)
	}
	merged, err := ApplyOverride(builtinPlugin(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Kind != "tool" || merged.Essential {
		t.Fatalf("merged kind=%q essential=%v, want the shipped tool/false", merged.Kind, merged.Essential)
	}

	// A sparse row written before these were server-owned is read the same way.
	stale := `{"$sparse":true,"kind":"hook","essential":true,"display_name":"x"}`
	if owned, err := OwnedFields(stale); err != nil || !slices.Equal(owned, []string{"display_name"}) {
		t.Fatalf("OwnedFields(stale) = %v, %v; want [display_name]", owned, err)
	}
}
