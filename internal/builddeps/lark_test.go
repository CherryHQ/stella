package builddeps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/resources"
)

func TestUpdateLarkMarkdownReferences(t *testing.T) {
	input := strings.Join([]string{
		"See ../lark-shared/SKILL.md",
		"See ../lark-shared/references/auth.md",
		"Use ./references/local.md",
		"See [auth](references/local.md)",
		"Code `references/local.md`",
		// ref already scoped to this skill (skillName/ prefix) — must not double-prefix
		"Use ./references/lark-doc/sub.md",
	}, "\n")
	got := updateLarkMarkdownReferences(input, "lark-doc")
	checks := []string{
		"./lark-shared.md",
		"./lark-shared/auth.md",
		"./lark-doc/local.md",
		"](./lark-doc/local.md)",
		"`./lark-doc/local.md`",
		"./references/lark-doc/sub.md",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("updated markdown missing %q:\n%s", check, got)
		}
	}
}

func TestGenerateLarkSkill(t *testing.T) {
	src := t.TempDir()
	writeSkillFixture(t, filepath.Join(src, "lark-shared"), "lark-shared", "Shared setup", "Auth in ./references/auth.md")
	writeSkillFixture(t, filepath.Join(src, "lark-doc"), "lark-doc", "Docs module", "See ../lark-shared/SKILL.md")
	dest := filepath.Join(t.TempDir(), "lark")
	if err := GenerateLarkSkill(src, dest, "test-ref"); err != nil {
		t.Fatalf("GenerateLarkSkill() error = %v", err)
	}
	main, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	mainStr := string(main)
	if !strings.Contains(mainStr, "name: lark") || !strings.Contains(mainStr, "source_ref: \"test-ref\"") {
		t.Fatalf("aggregate skill missing expected frontmatter:\n%s", mainStr)
	}
	if !strings.Contains(mainStr, "references/lark-shared.md") || !strings.Contains(mainStr, "references/lark-doc.md") {
		t.Fatalf("aggregate skill missing reference links:\n%s", mainStr)
	}
	child, err := os.ReadFile(filepath.Join(dest, "references", "lark-doc.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(child), "./lark-shared.md") {
		t.Fatalf("expected rewritten child markdown, got:\n%s", string(child))
	}
	ref, err := os.ReadFile(filepath.Join(dest, "references", "lark-shared", "auth.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ref) != "auth ref" {
		t.Fatalf("reference copy = %q, want auth ref", string(ref))
	}
}

func TestGenerateLarkSkillProducesDiscoverableBuiltin(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillFixture(t, filepath.Join(src, "lark-shared"), "lark-shared", "Shared setup", "Auth in ./references/auth.md")
	dest := filepath.Join(root, "skills", "system", "lark")
	if err := GenerateLarkSkill(src, dest, "test-ref"); err != nil {
		t.Fatalf("GenerateLarkSkill() error = %v", err)
	}
	reg, err := resources.Load(os.DirFS(root))
	if err != nil {
		t.Fatalf("resources.Load() error = %v", err)
	}
	if _, ok := reg.Get(resources.KindSkill, "lark"); !ok {
		t.Fatal("generated lark skill not discoverable")
	}
}

func writeSkillFixture(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := strings.Join([]string{
		"---",
		"name: " + name,
		"description: " + desc,
		"---",
		"",
		body,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "auth.md"), []byte("auth ref"), 0o644); err != nil {
		t.Fatal(err)
	}
}
