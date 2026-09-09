package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
)

func TestFilesystemSkillTurnViewPinsBytesAndRechecksPEP(t *testing.T) {
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000124"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'file-turn@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES('file-turn-agent','File turn','')`); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	skillDir := filepath.Join(base, "users", userID, ".agents", "skills", "demo")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(skillDir, MainFile)
	refPath := filepath.Join(skillDir, "references", "guide.md")
	if err := os.WriteFile(mainPath, []byte("---\nname: demo\ndescription: A demo skill\n---\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refPath, []byte("guide old"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(nil, manager)
	view, err := CaptureSkillTurnView(t.Context(), store, allowAllSkillReads{}, nil, nil, ViewContext{UserID: userID, AgentID: "file-turn-agent"})
	if err != nil {
		t.Fatal(err)
	}
	managed := view.ManagedIdentities()
	if len(managed) != 1 {
		t.Fatalf("captured managed skills = %#v", managed)
	}
	if err := os.WriteFile(mainPath, []byte("---\nname: demo\ndescription: A demo skill\n---\nnew\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refPath, []byte("guide new"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := newProjectionTool(t, store, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{})
	old, err := skillAction(tool, "load").Execute(WithSkillTurnView(t.Context(), view), map[string]any{"name": "demo", "path": "references/guide.md"})
	if err != nil || !strings.Contains(old, "guide old") || strings.Contains(old, "guide new") {
		t.Fatalf("old turn load = %q, %v", old, err)
	}

	next, err := CaptureSkillTurnView(t.Context(), store, allowAllSkillReads{}, nil, nil, ViewContext{UserID: userID, AgentID: "file-turn-agent"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := skillAction(tool, "load").Execute(WithSkillTurnView(t.Context(), next), map[string]any{"name": "demo"})
	if err != nil || !strings.Contains(updated, "new") || strings.Contains(updated, "old\n") {
		t.Fatalf("new turn load = %q, %v", updated, err)
	}

	if err := os.RemoveAll(skillDir); err != nil {
		t.Fatal(err)
	}
	deleted, err := CaptureSkillTurnView(t.Context(), store, allowAllSkillReads{}, nil, nil, ViewContext{UserID: userID, AgentID: "file-turn-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if got := deleted.ManagedIdentities(); len(got) != 0 {
		t.Fatalf("deleted source remained visible: %#v", got)
	}

	denied := newProjectionTool(t, store, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, selectedSkillReads{denied: map[string]bool{managed[0].ID: true}})
	if out, err := skillAction(denied, "load").Execute(WithSkillTurnView(t.Context(), view), map[string]any{"name": "demo"}); out != "" || !errors.Is(err, errSkillNotFound) {
		t.Fatalf("revoked old turn load = %q, %v; want hidden", out, err)
	}
}

func TestCaptureVisibleSkipsUnboundPersonalRoots(t *testing.T) {
	db := dbtest.New(t)
	const agentID = "group-file-agent"
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'Group file agent','')`, agentID); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	writeSkill := func(root home.RootOperations, name string) {
		t.Helper()
		content := "---\nname: " + name + "\ndescription: " + name + "\n---\n"
		if err := root.Mkdir(t.Context(), "skills", 0o755, home.MkdirOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := root.Mkdir(t.Context(), "skills/"+name, 0o755, home.MkdirOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := root.Write(t.Context(), "skills/"+name+"/"+MainFile, strings.NewReader(content), home.WriteOptions{Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
	}
	system, err := manager.OpenRoot(t.Context(), home.WorkspaceRequest{}, home.RootSystemResources, home.RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(system, "global")
	if err := system.Close(); err != nil {
		t.Fatal(err)
	}
	systemAgent, err := manager.OpenRoot(t.Context(), home.WorkspaceRequest{AgentID: agentID}, home.RootSystemAgentResources, home.RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(systemAgent, "agent-global")
	if err := systemAgent.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(nil, manager)
	captured, err := store.CaptureVisible(t.Context(), ViewContext{AgentID: agentID})
	if err != nil {
		t.Fatalf("group-style capture = %v", err)
	}
	if got := len(captured.Revisions); got != 2 {
		t.Fatalf("group-style revisions = %d, want system and system_agent only", got)
	}

	globalOnly, err := store.CaptureVisible(t.Context(), ViewContext{})
	if err != nil {
		t.Fatalf("global-only capture = %v", err)
	}
	if got := len(globalOnly.Revisions); got != 1 || globalOnly.Revisions[0].Skill.Name != "global" {
		t.Fatalf("global-only revisions = %#v, want only system resource", globalOnly.Revisions)
	}
}
