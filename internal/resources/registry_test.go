package resources

import (
	"testing"
	"testing/fstest"
)

func TestDefaultLoadsBuiltinResources(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	anna, ok := r.Get(KindSkill, "anna")
	if !ok {
		t.Fatal("expected builtin skill 'anna' to be loaded")
	}
	if anna.Name != "anna" {
		t.Errorf("skill name = %q, want %q", anna.Name, "anna")
	}
	if anna.Hash == "" {
		t.Error("skill hash is empty")
	}

	if _, ok := r.Get(KindSoul, "anna"); !ok {
		t.Error("expected builtin soul 'anna'")
	}
	if _, ok := r.Get(KindTemplate, "anna"); !ok {
		t.Error("expected builtin template 'anna'")
	}
	for _, id := range []string{"coder"} {
		if _, ok := r.Get(KindSubAgent, id); !ok {
			t.Errorf("expected builtin subagent %q", id)
		}
	}
}

func TestLoadWithFixture(t *testing.T) {
	fs := fstest.MapFS{
		"skills/demo/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: demo\ndescription: Demo skill\ntags: [x, y]\n---\nbody\n")},
		"souls/terse.md":       &fstest.MapFile{Data: []byte("---\nid: terse\nname: Terse\n---\nshort\n")},
		"subagents/runner.md":  &fstest.MapFile{Data: []byte("---\nname: runner\ntools: [bash]\nmax_turns: 5\n---\ngo\n")},
		"templates/blank.md":   &fstest.MapFile{Data: []byte("---\nid: blank\nname: Blank\nsoul_id: terse\n---\n")},
	}

	r, err := Load(fs)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	demo, ok := r.Get(KindSkill, "demo")
	if !ok {
		t.Fatal("demo skill missing")
	}
	if got := demo.Tags; len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("tags = %v, want [x y]", got)
	}

	runner, ok := r.Get(KindSubAgent, "runner")
	if !ok {
		t.Fatal("runner subagent missing")
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
	list := r.List(KindSubAgent)
	for i := 1; i < len(list); i++ {
		if list[i-1].ID > list[i].ID {
			t.Errorf("list not sorted: %q > %q", list[i-1].ID, list[i].ID)
		}
	}
}
