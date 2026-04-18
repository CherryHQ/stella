package skills

import (
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
