package skill

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/CherryHQ/stella/resources"
)

func TestServiceMergesExactSnapshotsByPrecedence(t *testing.T) {
	snapshot, err := SnapshotProjectSkills(t.Context(), snapshotRoot{fstest.MapFS{
		".agents/skills/shared/SKILL.md": {Data: []byte("---\nname: shared\ndescription: project winner\n---\n")},
	}}, ".")
	if err != nil {
		t.Fatal(err)
	}

	merged := NewService().ListMerged([]Skill{
		{ID: "managed-shared", Scope: "user", Name: "shared", Description: "managed loser"},
		{ID: "managed-stella", Scope: "user", Name: "stella", Description: "managed winner"},
	}, snapshot)

	byName := make(map[string]ResolvedSkill, len(merged))
	for _, skill := range merged {
		byName[skill.Name] = skill
	}
	if got := byName["shared"]; !got.IsImmutable() || got.Scope != "project" || got.Description != "project winner" {
		t.Fatalf("shared winner = %#v, want immutable project snapshot", got)
	}
	if got := byName["stella"]; got.IsImmutable() || got.ID != "managed-stella" {
		t.Fatalf("stella winner = %#v, want managed snapshot", got)
	}
}

func TestBuiltinRegistryReadsDoNotCreateRuntimeMirror(t *testing.T) {
	home := t.TempDir()
	registry, err := resources.Default()
	if err != nil {
		t.Fatal(err)
	}
	descriptor := registry.BuiltinSkills()[0]
	service := newService(registry)
	merged := service.ListMerged(nil, nil)

	var resolved *ResolvedSkill
	for i := range merged {
		if merged[i].Name == descriptor.Name {
			resolved = &merged[i]
			break
		}
	}
	if resolved == nil || !resolved.IsImmutable() {
		t.Fatalf("builtin %q missing from exact merge", descriptor.Name)
	}
	content, err := resolved.LoadBuiltinFile(MainFile)
	if err != nil || content == "" {
		t.Fatalf("LoadBuiltinFile = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(home, "bundles")); !os.IsNotExist(err) {
		t.Fatalf("registry read created a runtime mirror: %v", err)
	}
}

func TestBuiltinStableReferencesResolveToOneName(t *testing.T) {
	registry, err := resources.Default()
	if err != nil {
		t.Fatal(err)
	}
	descriptor := registry.BuiltinSkills()[0]
	service := newService(registry)
	for _, reference := range []string{descriptor.APIID, descriptor.Ref} {
		if got, ok := service.builtinNameForReference(reference); !ok || got != descriptor.Name {
			t.Fatalf("builtinNameForReference(%q) = %q, %v; want %q", reference, got, ok, descriptor.Name)
		}
	}
}

func TestAgentSkillPolicyFiltersOnlyResolvedWinner(t *testing.T) {
	merged := NewService().ListMerged([]Skill{
		{ID: "managed-stella", Scope: "system", Name: "stella", Status: SkillStatusActive},
	}, nil)
	if got := filterDisabled(merged, []string{"system:stella"}); len(got) != len(merged)-1 {
		t.Fatalf("filtered skills = %d, want %d", len(got), len(merged)-1)
	}
	for _, skill := range filterDisabled(merged, []string{"system:stella"}) {
		if skill.Name == "stella" {
			t.Fatal("disabled managed winner fell through to builtin stella")
		}
	}
}
