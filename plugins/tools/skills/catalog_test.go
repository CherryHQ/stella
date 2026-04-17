package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillsPriorityLowToHigh(t *testing.T) {
	t.Parallel()

	annaHome := t.TempDir()
	agentRoot := t.TempDir()
	userRoot := filepath.Join(t.TempDir(), "users", "7")
	cwd := t.TempDir()

	annaDir := filepath.Join(annaHome, "skills", "shared")
	agentDir := filepath.Join(agentRoot, ".agents", "skills", "shared")
	userDir := filepath.Join(userRoot, ".agents", "skills", "shared")
	projectDir := filepath.Join(cwd, ".agents", "skills", "shared")
	for _, dir := range []string{annaDir, agentDir, userDir, projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeSkill := func(dir, desc string) {
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: "+desc+"\nstatus: active\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(annaDir, "anna")
	writeSkill(agentDir, "agent")
	writeSkill(userDir, "user")
	writeSkill(projectDir, "project")

	skills := LoadSkills(context.Background(), LoadSkillsConfig{
		AnnaHome:    annaHome,
		AgentRoot:   agentRoot,
		UserRoot:    userRoot,
		ProjectRoot: cwd,
	})

	var shared Skill
	var found bool
	for _, skill := range skills {
		if skill.Name != "shared" {
			continue
		}
		shared = skill
		found = true
		break
	}
	if !found {
		t.Fatal("expected shared skill to be discovered")
	}
	if shared.Source != "project" {
		t.Fatalf("skill source = %q, want project", shared.Source)
	}
	if shared.Description != "project" {
		t.Fatalf("skill description = %q, want project", shared.Description)
	}
}
