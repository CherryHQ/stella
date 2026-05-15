package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeSkillMD writes a skill directory with a minimal SKILL.md frontmatter.
func writeSkillMD(t *testing.T, base, name, desc string) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\nstatus: active\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadFSSkillsPreservesPerScopeDuplicates verifies that when a user and
// an agent both define a skill with the same name on disk, both entries are
// retained for migration. Before the fix, the user entry silently overwrote
// the agent entry (last-wins by name), erasing layered overrides.
func TestLoadFSSkillsPreservesPerScopeDuplicates(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent", ".agents", "skills")
	userDir := filepath.Join(root, "user", ".agents", "skills")

	writeSkillMD(t, agentDir, "shared", "agent-version")
	writeSkillMD(t, userDir, "shared", "user-version")
	writeSkillMD(t, agentDir, "agent-only", "a")
	writeSkillMD(t, userDir, "user-only", "u")

	loaded := loadFSSkills(MigrateFSConfig{
		AgentRoot:     filepath.Join(root, "agent"),
		UserSkillsDir: userDir,
	})

	bySourceName := map[string]string{}
	for _, s := range loaded {
		bySourceName[s.Source+"/"+s.Name] = s.Description
	}

	if bySourceName["agent/shared"] != "agent-version" {
		t.Errorf("agent/shared = %q, want agent-version", bySourceName["agent/shared"])
	}
	if bySourceName["user/shared"] != "user-version" {
		t.Errorf("user/shared = %q, want user-version", bySourceName["user/shared"])
	}
	if _, ok := bySourceName["agent/agent-only"]; !ok {
		t.Error("missing agent-only entry")
	}
	if _, ok := bySourceName["user/user-only"]; !ok {
		t.Error("missing user-only entry")
	}
	if len(loaded) != 4 {
		t.Errorf("got %d loaded skills, want 4", len(loaded))
	}
}

// TestMigrateFilesystemRoundTrip exercises the full on-disk → DB path,
// including the AgentID wiring (fix 1) and the per-scope dedupe (fix 2).
// It also verifies idempotency: a second run reports Skipped, not Imported.
func TestMigrateFilesystemRoundTrip(t *testing.T) {
	store, db := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	ctx := context.Background()

	root := t.TempDir()
	agentSkills := filepath.Join(root, "agent", ".agents", "skills")
	userSkills := filepath.Join(root, "user", ".agents", "skills")

	writeSkillMD(t, agentSkills, "shared", "agent-version")
	writeSkillMD(t, userSkills, "shared", "user-version")
	writeSkillMD(t, agentSkills, "agent-only", "a-only")

	cfg := MigrateFSConfig{
		AgentRoot:     filepath.Join(root, "agent"),
		AgentID:       agentID,
		UserSkillsDir: userSkills,
		UserID:        userID,
	}

	res, err := MigrateFilesystem(ctx, store, cfg)
	if err != nil {
		t.Fatalf("MigrateFilesystem: %v", err)
	}
	if res.Imported != 3 || res.Skipped != 0 {
		t.Fatalf("first run: imported=%d skipped=%d, want 3/0", res.Imported, res.Skipped)
	}

	// Verify agent-scoped "shared" exists and carries the right AgentID.
	agentShared, err := store.Resolve(ctx, "shared", ViewContext{AgentID: agentID})
	if err != nil || agentShared == nil {
		t.Fatalf("resolve agent shared: %v (sk=%v)", err, agentShared)
	}
	// user precedence dominates when both are visible; scope-specific list works:
	list, err := store.List(ctx, ViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var hasAgentShared, hasUserShared bool
	for _, s := range list {
		if s.Name != "shared" {
			continue
		}
		switch s.Scope {
		case "agent":
			hasAgentShared = true
			if s.AgentID != agentID {
				t.Errorf("agent-scoped shared has AgentID=%q, want %q", s.AgentID, agentID)
			}
		case "user":
			hasUserShared = true
			if s.UserID != userID {
				t.Errorf("user-scoped shared has UserID=%q, want %q", s.UserID, userID)
			}
		}
	}
	if !hasAgentShared || !hasUserShared {
		t.Errorf("both scopes expected, got agent=%v user=%v", hasAgentShared, hasUserShared)
	}

	// Second run: everything should be Skipped, nothing Imported.
	res2, err := MigrateFilesystem(ctx, store, cfg)
	if err != nil {
		t.Fatalf("second MigrateFilesystem: %v", err)
	}
	if res2.Imported != 0 || res2.Skipped != 3 {
		t.Errorf("second run: imported=%d skipped=%d, want 0/3", res2.Imported, res2.Skipped)
	}
}
