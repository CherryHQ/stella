package skills

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"
)

// testSkillMD builds a minimal SKILL.md with the given frontmatter description.
func testSkillMD(description string) string {
	return "---\nname: foo\ndescription: " + description + "\nstatus: active\n---\n\n# Foo\n"
}

func TestSyncBuiltin(t *testing.T) {
	ctx := context.Background()

	// Build a fake embedded FS with one skill and one non-skill directory.
	baseFS := func(skillMDContent, refContent string) fstest.MapFS {
		m := fstest.MapFS{
			"foo/SKILL.md":         {Data: []byte(skillMDContent)},
			"foo/references/a.md":  {Data: []byte(refContent)},
			"agents/researcher.md": {Data: []byte("# Researcher\n")},
		}
		return m
	}

	t.Run("initial sync creates skill and files", func(t *testing.T) {
		store, _ := newTestStore(t)
		fakeFS := baseFS(testSkillMD("test skill"), "reference content")

		if err := SyncBuiltin(ctx, store, fakeFS); err != nil {
			t.Fatalf("SyncBuiltin: %v", err)
		}

		// Verify skill row exists.
		sk, err := store.Resolve(ctx, "foo", ViewContext{})
		if err != nil || sk == nil {
			t.Fatalf("Resolve: %v (sk=%v)", err, sk)
		}
		if sk.Scope != "system" {
			t.Errorf("scope = %q, want system", sk.Scope)
		}
		if sk.Description != "test skill" {
			t.Errorf("description = %q, want %q", sk.Description, "test skill")
		}

		// Verify SKILL.md file.
		content, err := store.LoadFile(ctx, sk.ID, MainFile)
		if err != nil {
			t.Fatalf("LoadFile SKILL.md: %v", err)
		}
		if content != testSkillMD("test skill") {
			t.Errorf("SKILL.md content = %q", content)
		}

		// Verify references/a.md file.
		ref, err := store.LoadFile(ctx, sk.ID, "references/a.md")
		if err != nil {
			t.Fatalf("LoadFile references/a.md: %v", err)
		}
		if ref != "reference content" {
			t.Errorf("references/a.md = %q", ref)
		}

		// agents/ must NOT produce a skill row.
		agentSk, err := store.Resolve(ctx, "agents", ViewContext{})
		if err != nil {
			t.Fatalf("Resolve agents: %v", err)
		}
		if agentSk != nil {
			t.Errorf("expected no skill for 'agents' dir, got one: %+v", agentSk)
		}
	})

	t.Run("second sync with changed content updates file", func(t *testing.T) {
		store, _ := newTestStore(t)
		fakeFS := baseFS(testSkillMD("test skill"), "original reference")

		if err := SyncBuiltin(ctx, store, fakeFS); err != nil {
			t.Fatalf("first SyncBuiltin: %v", err)
		}

		// Change the reference file content.
		fakeFS["foo/references/a.md"] = &fstest.MapFile{Data: []byte("updated reference")}

		if err := SyncBuiltin(ctx, store, fakeFS); err != nil {
			t.Fatalf("second SyncBuiltin: %v", err)
		}

		sk, _ := store.Resolve(ctx, "foo", ViewContext{})
		ref, err := store.LoadFile(ctx, sk.ID, "references/a.md")
		if err != nil {
			t.Fatalf("LoadFile after update: %v", err)
		}
		if ref != "updated reference" {
			t.Errorf("references/a.md = %q, want %q", ref, "updated reference")
		}
	})

	t.Run("second sync with removed file deletes orphan", func(t *testing.T) {
		store, _ := newTestStore(t)
		fakeFS := baseFS(testSkillMD("test skill"), "reference content")

		if err := SyncBuiltin(ctx, store, fakeFS); err != nil {
			t.Fatalf("first SyncBuiltin: %v", err)
		}

		// Remove the reference file from the fake FS.
		reducedFS := fstest.MapFS{
			"foo/SKILL.md":         {Data: []byte(testSkillMD("test skill"))},
			"agents/researcher.md": {Data: []byte("# Researcher\n")},
		}

		if err := SyncBuiltin(ctx, store, reducedFS); err != nil {
			t.Fatalf("second SyncBuiltin: %v", err)
		}

		sk, _ := store.Resolve(ctx, "foo", ViewContext{})
		_, err := store.LoadFile(ctx, sk.ID, "references/a.md")
		if err == nil {
			t.Fatal("expected error loading deleted file, got nil")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("expected sql.ErrNoRows, got: %v", err)
		}

		// SKILL.md still present.
		if _, err := store.LoadFile(ctx, sk.ID, MainFile); err != nil {
			t.Errorf("SKILL.md should still exist: %v", err)
		}
	})

	t.Run("agents dir without SKILL.md is not imported as skill", func(t *testing.T) {
		store, _ := newTestStore(t)
		fakeFS := fstest.MapFS{
			"foo/SKILL.md":         {Data: []byte(testSkillMD("test skill"))},
			"agents/coder.md":      {Data: []byte("# Coder\n")},
			"agents/researcher.md": {Data: []byte("# Researcher\n")},
		}

		if err := SyncBuiltin(ctx, store, fakeFS); err != nil {
			t.Fatalf("SyncBuiltin: %v", err)
		}

		// Only "foo" should be in the DB.
		skills, err := store.List(ctx, ViewContext{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(skills) != 1 {
			t.Errorf("expected 1 skill, got %d: %v", len(skills), skills)
		}
		if skills[0].Name != "foo" {
			t.Errorf("expected skill 'foo', got %q", skills[0].Name)
		}
	})
}
