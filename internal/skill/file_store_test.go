package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestFilesystemSkillEditing(t *testing.T) {
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000123"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'file-skill@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES('file-skill-agent','File skills','')`); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	root := filepath.Join(base, "users", userID, ".agents")
	if err := os.MkdirAll(filepath.Join(root, "skills", "demo", "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := "---\nname: demo\ndescription: A demo skill\nmetadata:\n  version: v2\n  created_by: forged\n---\nfirst\n"
	if err := os.WriteFile(filepath.Join(root, "skills", "demo", MainFile), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "demo", "references", "guide.md"), []byte("guide one"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(nil, manager)
	first, err := store.CaptureVisible(t.Context(), ViewContext{UserID: userID, AgentID: "file-skill-agent"})
	if err != nil || len(first.Revisions) != 1 {
		t.Fatalf("first capture = %#v, err=%v", first, err)
	}
	if strings.Contains(first.Revisions[0].Skill.ContentDigest, "sha256:") || strings.Contains(string(first.Revisions[0].Skill.Metadata), "created_by") {
		t.Fatalf("capture leaked digest prefix or forged metadata: %#v", first.Revisions[0].Skill)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "demo", MainFile), []byte(strings.Replace(main, "first", "second", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "demo", "references", "guide.md"), []byte("guide two"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := store.CaptureVisible(t.Context(), ViewContext{UserID: userID, AgentID: "file-skill-agent"})
	if err != nil || len(second.Revisions) != 1 {
		t.Fatalf("second capture = %#v, err=%v", second, err)
	}
	if first.Revisions[0].Skill.ContentDigest == second.Revisions[0].Skill.ContentDigest || string(first.Revisions[0].Files[MainFile]) != main || string(first.Revisions[0].Files["references/guide.md"]) != "guide one" {
		t.Fatal("capture was not immutable or edit was not observed")
	}

	if got, err := store.GetIdentity(t.Context(), fileSkillID("user_agent", userID, "file-skill-agent", "../escape")); err != nil || got != nil {
		t.Fatalf("invalid file identity = %#v, err=%v", got, err)
	}
}

func TestFilesystemSkillCRUDScopeAndDiskState(t *testing.T) {
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000124"
	const agentID = "file-crud-agent"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'file-crud@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'File CRUD','')`, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store := NewFileStore(db, manager)
	ctx := t.Context()

	userSkill, err := store.CreateManagedSkill(ctx, Skill{Scope: "user", UserID: userID, Name: "shared-skill", Description: "user version"}, map[string]string{MainFile: "# user\n"})
	if err != nil {
		t.Fatal(err)
	}
	agentSkill, err := store.CreateManagedSkill(ctx, Skill{Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "shared-skill", Description: "agent version"}, map[string]string{MainFile: "# agent\n"})
	if err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(base, "users", userID, ".agents", "skills", userSkill.Skill.Name, MainFile)
	agentPath := filepath.Join(base, "users", userID, "agents", agentID, ".agents", "skills", agentSkill.Skill.Name, MainFile)
	for _, filename := range []string{userPath, agentPath} {
		if data, err := os.ReadFile(filename); err != nil || !strings.Contains(string(data), "name: shared-skill") {
			t.Fatalf("created file %s = %q, err=%v", filename, data, err)
		}
	}
	staging := filepath.Join(base, "users", userID, ".agents", "skills", ".stella-skill-tmp-test")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, MainFile), []byte("broken staging content"), 0o644); err != nil {
		t.Fatal(err)
	}

	userScope, err := store.ListIdentityByScope(ctx, "user", userID, "")
	if err != nil || len(userScope) != 1 || userScope[0].ID != userSkill.Skill.ID {
		t.Fatalf("user scope = %#v, err=%v", userScope, err)
	}
	agentScope, err := store.ListIdentityByScope(ctx, "user_agent", userID, agentID)
	if err != nil || len(agentScope) != 1 || agentScope[0].ID != agentSkill.Skill.ID {
		t.Fatalf("user-agent scope = %#v, err=%v", agentScope, err)
	}
	visible, err := store.ListIdentityVisible(ctx, ViewContext{UserID: userID, AgentID: agentID})
	if err != nil || len(visible) != 1 || visible[0].ID != agentSkill.Skill.ID {
		t.Fatalf("visible precedence = %#v, err=%v", visible, err)
	}

	full := "---\nname: shared-skill\ndescription: incoming description\nstatus: active\ndisable-model-invocation: true\nmetadata:\n  incoming: yes\ncustom: retained\n---\n# incoming\n"
	updated, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: agentSkill.Skill.ID, Scope: "user_agent", UserID: userID, AgentID: agentID,
		ExpectedDigest: agentSkill.Skill.ContentDigest, Files: map[string]string{MainFile: full},
		Patch: UpdatePatch{Description: stringPtr("patched description")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Skill.Description != "patched description" || !updated.Skill.DisableModelInvocation || string(updated.Skill.Metadata) != `{"incoming":"yes"}` {
		t.Fatalf("full document plus patch = %#v", updated.Skill)
	}
	disk, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disk), "description: patched description") || !strings.Contains(string(disk), "custom: retained") || !strings.Contains(string(disk), "# incoming") {
		t.Fatalf("updated disk file = %q", disk)
	}
	if _, err := store.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: agentSkill.Skill.ID, Scope: "user_agent", UserID: userID, AgentID: agentID, ExpectedDigest: agentSkill.Skill.ContentDigest, Files: map[string]string{MainFile: "# stale\n"}}); !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("stale update = %v, want digest conflict", err)
	}
	if err := store.DeleteManagedSkill(ctx, ManagedSkillDelete{ID: updated.Skill.ID, Scope: "user_agent", UserID: userID, AgentID: agentID, ExpectedDigest: updated.Skill.ContentDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted disk file stat = %v", err)
	}
	visible, err = store.ListIdentityVisible(ctx, ViewContext{UserID: userID, AgentID: agentID})
	if err != nil || len(visible) != 1 || visible[0].ID != userSkill.Skill.ID {
		t.Fatalf("visible after delete = %#v, err=%v", visible, err)
	}
	var evidence int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_changelog WHERE skill_id=$1`, updated.Skill.ID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != 3 {
		t.Fatalf("file evidence rows = %d, want 3", evidence)
	}
}

func TestFilesystemSkillRejectsUndiscoverableResourceBeforeWriting(t *testing.T) {
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000125"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'file-limit@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store := NewFileStore(db, manager)
	large := strings.Repeat("x", plugin.ResourceMaxFileBytes+1)
	_, err = store.CreateManagedSkill(t.Context(), Skill{Scope: "user", UserID: userID, Name: "too-large", Description: "rejected"}, map[string]string{MainFile: large})
	if !errors.Is(err, ErrSkillLimit) {
		t.Fatalf("large create = %v, want ErrSkillLimit", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, "users", userID, ".agents", "skills", "too-large")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("large create left disk state: %v", statErr)
	}
}

func TestParseFrontmatterEmptyAndMetadata(t *testing.T) {
	fm, err := parseFrontmatter("---\n---\n")
	if err != nil || fm.Status != SkillStatusActive {
		t.Fatalf("empty frontmatter = %#v, err=%v", fm, err)
	}
	fm, err = parseFrontmatter("---\nname: demo\ndescription: demo\nmetadata:\n  nested:\n    enabled: true\n  created_by: forged\n---\n")
	if err != nil || fm.Metadata["created_by"] != nil {
		t.Fatalf("metadata parsing = %#v, err=%v", fm, err)
	}
}

func TestRenderSkillMarkdownUpdateInputs(t *testing.T) {
	sk := Skill{Name: "demo", Description: "new description", Status: SkillStatusActive, Metadata: []byte(`{"version":"v2"}`)}
	existing := []byte("---\nname: demo\ndescription: old\ncustom: keep\n---\nold body\n")
	plain, err := renderSkillMarkdown(sk, existing, []byte("new body\n"))
	if err != nil || !strings.Contains(string(plain), "custom: keep") || !strings.Contains(string(plain), "new body") || strings.Contains(string(plain), "old body") {
		t.Fatalf("plain update = %q, err=%v", plain, err)
	}
	full, err := renderSkillMarkdown(sk, existing, []byte("---\nname: demo\ndescription: incoming\ncustom: replace\n---\nincoming body\n"))
	if err != nil || !strings.Contains(string(full), "custom: replace") || !strings.Contains(string(full), "incoming body") || strings.Contains(string(full), "custom: keep") {
		t.Fatalf("full update = %q, err=%v", full, err)
	}
	if _, err := renderSkillMarkdown(sk, existing, []byte("---\ncustom: [broken\n---\nbody")); err == nil {
		t.Fatal("invalid incoming frontmatter was accepted")
	}
}
