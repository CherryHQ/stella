package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/pkg/memory/memorytest"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    skillFrontmatter
		wantErr bool
	}{
		{
			name:    "valid frontmatter",
			content: "---\nname: my-skill\ndescription: Does things\n---\n# Body",
			want:    skillFrontmatter{Name: "my-skill", Description: "Does things"},
		},
		{
			name:    "with disable-model-invocation",
			content: "---\nname: hidden\ndescription: Secret\ndisable-model-invocation: true\n---\n",
			want:    skillFrontmatter{Name: "hidden", Description: "Secret", DisableModelInvocation: true},
		},
		{
			name:    "no frontmatter",
			content: "# Just markdown",
			wantErr: true,
		},
		{
			name:    "unclosed frontmatter",
			content: "---\nname: broken\n# no closing",
			wantErr: true,
		},
		{
			name:    "windows line endings",
			content: "---\r\nname: win\r\ndescription: Windows skill\r\n---\r\nBody",
			want:    skillFrontmatter{Name: "win", Description: "Windows skill"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFrontmatter(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.want.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.want.Name)
			}
			if got.Description != tt.want.Description {
				t.Errorf("Description = %q, want %q", got.Description, tt.want.Description)
			}
			if got.DisableModelInvocation != tt.want.DisableModelInvocation {
				t.Errorf("DisableModelInvocation = %v, want %v", got.DisableModelInvocation, tt.want.DisableModelInvocation)
			}
		})
	}
}

func TestLoadSkillsFromDir(t *testing.T) {
	// Create temp skill directory structure:
	// skills/
	//   root-skill.md          (root .md file, should be picked up)
	//   my-skill/
	//     SKILL.md             (subdirectory with SKILL.md)
	//   no-desc/
	//     SKILL.md             (missing description, should be skipped)
	//   .hidden/
	//     SKILL.md             (hidden dir, should be skipped)
	//   nested/
	//     deep-skill/
	//       SKILL.md           (deeply nested)

	dir := t.TempDir()

	writeSkill := func(relPath, content string) {
		t.Helper()
		full := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeSkill("root-skill.md", "---\nname: root-skill\ndescription: A root skill\n---\n# Root")
	writeSkill("my-skill/SKILL.md", "---\nname: my-skill\ndescription: My cool skill\n---\n# Cool")
	writeSkill("no-desc/SKILL.md", "---\nname: no-desc\n---\n# Missing description")
	writeSkill(".hidden/SKILL.md", "---\nname: hidden\ndescription: Should be hidden\n---\n")
	writeSkill("nested/deep-skill/SKILL.md", "---\nname: deep-skill\ndescription: Deep one\n---\n")

	skills := loadSkillsFromDir(dir, "test")

	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}

	if !names["root-skill"] {
		t.Error("expected root-skill to be loaded")
	}
	if !names["my-skill"] {
		t.Error("expected my-skill to be loaded")
	}
	if !names["deep-skill"] {
		t.Error("expected deep-skill to be loaded")
	}
	if names["no-desc"] {
		t.Error("no-desc should be skipped (missing description)")
	}
	if names["hidden"] {
		t.Error(".hidden dir should be skipped")
	}
}

func TestLoadSkillsFallbackName(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "fallback-name")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: No name field\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := loadSkillsFromDir(dir, "test")
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "fallback-name" {
		t.Errorf("expected name fallback to dir name 'fallback-name', got %q", skills[0].Name)
	}
}

func TestLoadSkillsDedup(t *testing.T) {
	// Same skill name in project .agents/ and workspace — project wins (highest priority).
	wsDir := t.TempDir()
	projectDir := t.TempDir()

	// workspace/skills/dupe-skill
	if err := os.MkdirAll(filepath.Join(wsDir, "skills", "dupe-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "skills", "dupe-skill", "SKILL.md"),
		[]byte("---\nname: dupe-skill\ndescription: Workspace version\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// cwd/.agents/skills/dupe-skill (project-level, highest priority)
	projAgents := filepath.Join(projectDir, ".agents")
	if err := os.MkdirAll(filepath.Join(projAgents, "skills", "dupe-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projAgents, "skills", "dupe-skill", "SKILL.md"),
		[]byte("---\nname: dupe-skill\ndescription: Project version\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := loadSkills("/nonexistent/home", "/nonexistent/anna", wsDir, projectDir, "")

	count := 0
	for _, s := range skills {
		if s.Name == "dupe-skill" {
			count++
			// project .agents/ is scanned first, so it wins
			if s.Description != "Project version" {
				t.Errorf("expected project version to win, got description %q", s.Description)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 dupe-skill, got %d", count)
	}
}

func TestVisibleSkills(t *testing.T) {
	skills := []Skill{
		{Name: "web-search", Description: "Search the web"},
		{Name: "hidden", Description: "Secret skill", DisableModelInvocation: true},
		{Name: "code-review", Description: "Review code"},
		{Name: "old", Description: "Old skill", Status: SkillStatusDeprecated},
	}

	visible := VisibleSkills(skills)

	if len(visible) != 2 {
		t.Fatalf("expected 2 visible skills, got %d", len(visible))
	}
	names := map[string]bool{}
	for _, s := range visible {
		names[s.Name] = true
	}
	if !names["web-search"] {
		t.Error("expected web-search in visible")
	}
	if !names["code-review"] {
		t.Error("expected code-review in visible")
	}
	if names["hidden"] {
		t.Error("hidden skill should be excluded (DisableModelInvocation)")
	}
	if names["old"] {
		t.Error("deprecated skill should be excluded")
	}
}

func TestVisibleSkillsEmpty(t *testing.T) {
	result := VisibleSkills(nil)
	if len(result) != 0 {
		t.Errorf("expected empty for nil skills, got %d", len(result))
	}

	result = VisibleSkills([]Skill{{Name: "x", Description: "y", DisableModelInvocation: true}})
	if len(result) != 0 {
		t.Errorf("expected empty when all skills are hidden, got %d", len(result))
	}
}

func TestValidateSkillName(t *testing.T) {
	// Valid
	errs := ValidateSkillName("web-search", "web-search")
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid name, got %v", errs)
	}

	// Name mismatch
	errs = ValidateSkillName("foo", "bar")
	if len(errs) == 0 {
		t.Error("expected error for name/dir mismatch")
	}

	// Invalid chars
	errs = ValidateSkillName("My_Skill", "My_Skill")
	found := false
	for _, e := range errs {
		if strings.Contains(e, "invalid characters") {
			found = true
		}
	}
	if !found {
		t.Error("expected invalid characters error")
	}

	// Leading hyphen
	errs = ValidateSkillName("-bad", "-bad")
	found = false
	for _, e := range errs {
		if strings.Contains(e, "start or end") {
			found = true
		}
	}
	if !found {
		t.Error("expected hyphen error")
	}

	// Consecutive hyphens
	errs = ValidateSkillName("bad--name", "bad--name")
	found = false
	for _, e := range errs {
		if strings.Contains(e, "consecutive") {
			found = true
		}
	}
	if !found {
		t.Error("expected consecutive hyphens error")
	}
}

func TestLoadSkillsProjectPriority(t *testing.T) {
	wsDir := t.TempDir()
	projectDir := t.TempDir()

	// Workspace skill: wsDir/skills/my-skill
	wsSkill := filepath.Join(wsDir, "skills", "my-skill")
	if err := os.MkdirAll(wsSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsSkill, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: Workspace version\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Project-level skill with same name: projectDir/.agents/skills/my-skill
	projSkill := filepath.Join(projectDir, ".agents", "skills", "my-skill")
	if err := os.MkdirAll(projSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projSkill, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: Project version\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := loadSkills("/nonexistent/home", "/nonexistent/anna", wsDir, projectDir, "")

	count := 0
	for _, s := range skills {
		if s.Name == "my-skill" {
			count++
			if s.Source != "project" {
				t.Errorf("expected project-level to win, got source %q", s.Source)
			}
			if s.Description != "Project version" {
				t.Errorf("expected project description, got %q", s.Description)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 my-skill, got %d", count)
	}
}

func TestLoadSkillsNonexistentDir(t *testing.T) {
	skills := loadSkills("/nonexistent/home", "/nonexistent/anna", "/nonexistent/agents", "/nonexistent/cwd", "")
	if len(skills) != 0 {
		t.Errorf("expected no skills for nonexistent dirs, got %d", len(skills))
	}
}

func TestBuildSystemPromptIncludesSkills(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "workspace")
	projectDir := filepath.Join(dir, "project")

	// Create a skill in the workspace dir
	skillDir := filepath.Join(wsDir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: test-skill\ndescription: A test skill for prompt integration\n---\n# Test"),
		0o644); err != nil {
		t.Fatal(err)
	}

	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{
		AnnaHome:  "/nonexistent/anna",
		Workspace: wsDir,
		Cwd:       projectDir,
	})
	if !strings.Contains(prompt, "<available_skills>") {
		t.Error("expected skills section in system prompt")
	}
	if !strings.Contains(prompt, "test-skill") {
		t.Error("expected test-skill in system prompt")
	}
}

func TestLoadProjectContextFiles(t *testing.T) {
	t.Run("finds AGENTS.md in cwd", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Project Rules"), 0o644); err != nil {
			t.Fatal(err)
		}

		files := loadProjectContextFiles(dir)
		if len(files) == 0 {
			t.Fatal("expected at least one context file")
		}
		found := false
		for _, f := range files {
			if strings.Contains(f.Content, "# Project Rules") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected to find AGENTS.md content")
		}
	})

	t.Run("walks ancestors in root-to-leaf order", func(t *testing.T) {
		root := t.TempDir()
		child := filepath.Join(root, "sub", "project")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(child, "AGENTS.md"), []byte("project rules"), 0o644); err != nil {
			t.Fatal(err)
		}

		files := loadProjectContextFiles(child)
		if len(files) < 2 {
			t.Fatalf("expected at least 2 context files, got %d", len(files))
		}
		// Root should come before child.
		rootIdx, childIdx := -1, -1
		for i, f := range files {
			if strings.Contains(f.Content, "root rules") {
				rootIdx = i
			}
			if strings.Contains(f.Content, "project rules") {
				childIdx = i
			}
		}
		if rootIdx == -1 || childIdx == -1 {
			t.Fatal("expected both root and child AGENTS.md files")
		}
		if rootIdx >= childIdx {
			t.Errorf("expected root (%d) before child (%d)", rootIdx, childIdx)
		}
	})

	t.Run("case insensitive match", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "agents.md"), []byte("lowercase agents"), 0o644); err != nil {
			t.Fatal(err)
		}

		files := loadProjectContextFiles(dir)
		found := false
		for _, f := range files {
			if strings.Contains(f.Content, "lowercase agents") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected case-insensitive AGENTS.md match")
		}
	})

	t.Run("empty cwd returns nil", func(t *testing.T) {
		files := loadProjectContextFiles("")
		if files != nil {
			t.Errorf("expected nil, got %v", files)
		}
	})
}

func TestBuildSystemPromptIncludesContextFiles(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "workspace")
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "AGENTS.md"),
		[]byte("Always use snake_case."), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{
		AnnaHome:  "/nonexistent/anna",
		Workspace: wsDir,
		Cwd:       projectDir,
	})

	if !strings.Contains(prompt, "# Project Context") {
		t.Error("expected Project Context section in system prompt")
	}
	if !strings.Contains(prompt, "Always use snake_case.") {
		t.Error("expected AGENTS.md content in system prompt")
	}
}

func TestBuildSystemPromptAgentSoulAndProfile(t *testing.T) {
	wsDir := t.TempDir()

	fake := memorytest.New()
	ctx := context.Background()

	// Set up agent soul and user profile.
	_ = fake.SetAgentSoul(ctx, 1, "test", "Be concise and friendly.")
	_ = fake.SetProfile(ctx, 1, "test", "User is a Go developer.")

	prompt := BuildSystemPromptFromDB(ctx, DBPromptParams{
		SystemPrompt: "You are Anna.",
		Memory:       fake,
		UserID:       1,
		AgentID:      "test",
		AnnaHome:     "/nonexistent/anna",
		Workspace:    wsDir,
	})

	// Base identity should be present.
	if !strings.Contains(prompt, "You are Anna.") {
		t.Error("expected base identity in prompt")
	}
	// Agent soul section.
	if !strings.Contains(prompt, "<agent_soul>") {
		t.Error("expected <agent_soul> tag in prompt")
	}
	if !strings.Contains(prompt, "Be concise and friendly.") {
		t.Error("expected agent soul content in prompt")
	}
	// User profile section.
	if !strings.Contains(prompt, "<user_profile>") {
		t.Error("expected <user_profile> tag in prompt")
	}
	if !strings.Contains(prompt, "User is a Go developer.") {
		t.Error("expected user profile content in prompt")
	}
}

func TestBuildSystemPromptEmptySoulAndProfile(t *testing.T) {
	wsDir := t.TempDir()

	fake := memorytest.New()
	ctx := context.Background()

	// No soul or profile set — sections should still appear.
	prompt := BuildSystemPromptFromDB(ctx, DBPromptParams{
		Memory:    fake,
		UserID:    1,
		AgentID:   "test",
		AnnaHome:  "/nonexistent/anna",
		Workspace: wsDir,
	})

	if !strings.Contains(prompt, "<agent_soul>") {
		t.Error("expected <agent_soul> tag even when empty")
	}
	if !strings.Contains(prompt, "</agent_soul>") {
		t.Error("expected </agent_soul> closing tag")
	}
	if !strings.Contains(prompt, "<user_profile>") {
		t.Error("expected <user_profile> tag even when empty")
	}
	if !strings.Contains(prompt, "</user_profile>") {
		t.Error("expected </user_profile> closing tag")
	}
}

func TestLoadSkillsWithUserDir(t *testing.T) {
	wsDir := t.TempDir()
	userSkillsDir := t.TempDir()

	mkSkill := func(dir, name, desc string) {
		sd := filepath.Join(dir, name)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Agent-level skill.
	mkSkill(filepath.Join(wsDir, "skills"), "shared-skill", "Agent version")
	mkSkill(filepath.Join(wsDir, "skills"), "agent-only", "Agent only")

	// User-level skill (same name overrides agent-level).
	mkSkill(userSkillsDir, "shared-skill", "User version")
	mkSkill(userSkillsDir, "user-only", "User only")

	skills := loadSkills("/nonexistent/home", "/nonexistent/anna", wsDir, "", userSkillsDir)

	byName := map[string]Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}

	// User version wins for shared skill.
	if s, ok := byName["shared-skill"]; !ok {
		t.Error("expected shared-skill")
	} else {
		if s.Description != "User version" {
			t.Errorf("expected user version to win, got %q", s.Description)
		}
		if s.Source != "user" {
			t.Errorf("expected source 'user', got %q", s.Source)
		}
	}

	// Both unique skills present.
	if _, ok := byName["agent-only"]; !ok {
		t.Error("expected agent-only")
	}
	if _, ok := byName["user-only"]; !ok {
		t.Error("expected user-only")
	}
}

func TestLoadSkillsUserDirEmpty(t *testing.T) {
	// When userSkillsDir is empty, behavior should be same as before.
	wsDir := t.TempDir()
	mkSkill := func(dir, name, desc string) {
		sd := filepath.Join(dir, name)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkSkill(filepath.Join(wsDir, "skills"), "ws-skill", "Workspace skill")

	skills := loadSkills("/nonexistent/home", "/nonexistent/anna", wsDir, "", "")
	found := false
	for _, s := range skills {
		if s.Name == "ws-skill" {
			found = true
			if s.Source != "agent" {
				t.Errorf("expected source 'agent', got %q", s.Source)
			}
		}
	}
	if !found {
		t.Error("expected ws-skill")
	}
}

func TestNormalizeSkillStatus(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", SkillStatusActive},
		{"active", SkillStatusActive},
		{"Active", SkillStatusActive},
		{"ACTIVE", SkillStatusActive},
		{"draft", SkillStatusDraft},
		{"Draft", SkillStatusDraft},
		{"deprecated", SkillStatusDeprecated},
		{"Deprecated", SkillStatusDeprecated},
		{"  active  ", SkillStatusActive},
		{"unknown", SkillStatusActive},
		{"bogus", SkillStatusActive},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%q", tt.input), func(t *testing.T) {
			got := NormalizeSkillStatus(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeSkillStatus(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestVisibleSkillsFiltersDeprecated(t *testing.T) {
	skills := []Skill{
		{Name: "active-skill", Description: "Active skill", Status: SkillStatusActive},
		{Name: "deprecated-skill", Description: "Old skill", Status: SkillStatusDeprecated},
		{Name: "draft-skill", Description: "Draft skill", Status: SkillStatusDraft},
	}

	visible := VisibleSkills(skills)

	names := map[string]bool{}
	for _, s := range visible {
		names[s.Name] = true
	}
	if !names["active-skill"] {
		t.Error("expected active-skill in visible")
	}
	if names["deprecated-skill"] {
		t.Error("deprecated-skill should be filtered out")
	}
	if !names["draft-skill"] {
		t.Error("expected draft-skill in visible")
	}
}

func TestVisibleSkillsAllDeprecated(t *testing.T) {
	skills := []Skill{
		{Name: "old", Description: "Old one", Status: SkillStatusDeprecated},
	}

	visible := VisibleSkills(skills)
	if len(visible) != 0 {
		t.Errorf("expected empty for all-deprecated skills, got %d", len(visible))
	}
}

func TestLoadSkillFromFileWithStatus(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "status-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: status-skill\ndescription: Has status\nstatus: draft\ncreated-at: \"2026-04-01T00:00:00Z\"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := loadSkillsFromDir(dir, "test")
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Status != SkillStatusDraft {
		t.Errorf("expected status 'draft', got %q", skills[0].Status)
	}
	if skills[0].CreatedAt != "2026-04-01T00:00:00Z" {
		t.Errorf("expected created-at, got %q", skills[0].CreatedAt)
	}
}

func TestLoadSkillFromFileDefaultStatus(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "no-status")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: no-status\ndescription: No status field\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := loadSkillsFromDir(dir, "test")
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Status != SkillStatusActive {
		t.Errorf("expected default status 'active', got %q", skills[0].Status)
	}
}

func TestLoadSkillsThreeTierPriority(t *testing.T) {
	homeDir := t.TempDir()
	wsDir := t.TempDir()
	projectDir := t.TempDir()

	mkSkill := func(dir, name, desc string) {
		sd := filepath.Join(dir, name)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Common skill (~/.agents/skills/)
	mkSkill(filepath.Join(homeDir, ".agents", "skills"), "shared-skill", "Common version")
	mkSkill(filepath.Join(homeDir, ".agents", "skills"), "common-only", "Only in common")

	// Workspace skill
	mkSkill(filepath.Join(wsDir, "skills"), "shared-skill", "Workspace version")
	mkSkill(filepath.Join(wsDir, "skills"), "ws-only", "Only in workspace")

	// Project skill (.agents/skills/)
	mkSkill(filepath.Join(projectDir, ".agents", "skills"), "shared-skill", "Project version")
	mkSkill(filepath.Join(projectDir, ".agents", "skills"), "proj-only", "Only in project")

	skills := loadSkills(homeDir, "/nonexistent/anna", wsDir, projectDir, "")

	byName := map[string]Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}

	// shared-skill: project wins
	if s, ok := byName["shared-skill"]; !ok {
		t.Error("expected shared-skill")
	} else if s.Description != "Project version" {
		t.Errorf("shared-skill: expected Project version, got %q", s.Description)
	}

	// All three unique skills present
	for _, name := range []string{"common-only", "ws-only", "proj-only"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("expected %s to be loaded", name)
		}
	}
}
