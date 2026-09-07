package resources

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestValidateBuiltinSkillOwnersUsesRuntimeCatalog(t *testing.T) {
	r := &Registry{skills: map[string]BuiltinSkillDescriptor{
		"owned":   {Name: "owned", Root: "plugins/agent/demo/owned", SourceRoot: "agent/demo/skills/owned", OwnerPluginID: "demo"},
		"foreign": {Name: "foreign", Root: "plugins/agent/other/foreign", SourceRoot: "agent/other/skills/foreign", OwnerPluginID: "other"},
	}}
	if err := r.ValidateBuiltinSkillOwners(map[string]struct{}{"demo": {}}); err == nil || !strings.Contains(err.Error(), "unknown plugin owner") {
		t.Fatalf("ValidateBuiltinSkillOwners() error = %v, want unknown owner", err)
	}
	if err := r.ValidateBuiltinSkillOwners(map[string]struct{}{"demo": {}, "other": {}}); err != nil {
		t.Fatalf("ValidateBuiltinSkillOwners() error = %v", err)
	}
}

func TestDefaultLoadsBuiltinResources(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	stella, ok := r.Get(KindSkill, "stella")
	if !ok {
		t.Fatal("expected builtin skill 'stella' to be loaded")
	}
	if stella.Name != "stella" {
		t.Errorf("skill name = %q, want %q", stella.Name, "stella")
	}
	if stella.Hash == "" {
		t.Error("skill hash is empty")
	}
	stellaDescriptor, ok := r.BuiltinSkill("stella")
	if !ok || stellaDescriptor.Ref != "builtin:stella" || stellaDescriptor.APIID != "builtin-stella" || stellaDescriptor.Digest == "" || len(stellaDescriptor.Files) == 0 {
		t.Fatalf("incomplete stella builtin descriptor: %#v", stellaDescriptor)
	}
	if _, _, err := r.ReadBuiltinSkillFile("stella", "SKILL.md"); err != nil {
		t.Fatalf("ReadBuiltinSkillFile(stella/SKILL.md): %v", err)
	}

	if _, ok := r.Get(KindSoul, "stella"); !ok {
		t.Error("expected builtin soul 'stella'")
	}
	if _, ok := r.Get(KindTemplate, "stella"); !ok {
		t.Error("expected builtin template 'stella'")
	}
	if err := r.ValidateBuiltinSkillOwners(map[string]struct{}{
		"email":         {},
		"recally":       {},
		"scheduler":     {},
		"stella":        {},
		"xberg":         {},
		"web":           {},
		"lark-cli":      {},
		"html-artifact": {},
		"skill-creator": {},
		"uv":            {},
	}); err != nil {
		t.Fatalf("ValidateBuiltinSkillOwners(): %v", err)
	}
	for _, skill := range r.BuiltinSkills() {
		if skill.Name == "stella" && skill.OwnerPluginID != "stella" {
			t.Fatalf("stella skill owner = %q, want stella", skill.OwnerPluginID)
		}
	}
	for _, id := range []string{"coder"} {
		if _, ok := r.Get(KindDelegate, id); !ok {
			t.Errorf("expected builtin delegate %q", id)
		}
	}
}

func TestLoadWithFixture(t *testing.T) {
	fs := fstest.MapFS{
		"souls/terse.md":      &fstest.MapFile{Data: []byte("---\nid: terse\nname: Terse\n---\nshort\n")},
		"delegates/runner.md": &fstest.MapFile{Data: []byte("---\nname: runner\ntools: [bash]\nmax_turns: 5\n---\ngo\n")},
		"templates/blank.md":  &fstest.MapFile{Data: []byte("---\nid: blank\nname: Blank\nsoul_id: terse\n---\n")},
	}

	r, err := loadResources(fs)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	runner, ok := r.Get(KindDelegate, "runner")
	if !ok {
		t.Fatal("runner delegate missing")
	}
	if runner.Metadata["max_turns"] != 5 {
		t.Errorf("max_turns = %v, want 5", runner.Metadata["max_turns"])
	}

	tpl, ok := r.Get(KindTemplate, "blank")
	if !ok {
		t.Fatal("blank template missing")
	}
	if tpl.Metadata["soul_id"] != "terse" {
		t.Errorf("soul_id = %v, want terse", tpl.Metadata["soul_id"])
	}
}

func TestListIsSortedByID(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	list := r.List(KindDelegate)
	for i := 1; i < len(list); i++ {
		if list[i-1].ID > list[i].ID {
			t.Errorf("list not sorted: %q > %q", list[i-1].ID, list[i].ID)
		}
	}
}
