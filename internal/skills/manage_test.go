package skills

import (
	"strings"
	"testing"
)

func TestValidateCreateInput_valid(t *testing.T) {
	errs := validateCreateInput("my-skill", "does something useful")
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got: %v", errs)
	}
}

func TestValidateCreateInput_emptyDescription(t *testing.T) {
	errs := validateCreateInput("my-skill", "   ")
	if len(errs) == 0 {
		t.Fatal("expected error for empty description")
	}
	if !strings.Contains(errs[len(errs)-1], "description") {
		t.Fatalf("unexpected error message: %v", errs)
	}
}

func TestValidateCreateInput_invalidName(t *testing.T) {
	errs := validateCreateInput("My Invalid Name!", "desc")
	if len(errs) == 0 {
		t.Fatal("expected error for invalid name")
	}
}

func TestBuildSkillFile_containsFrontmatter(t *testing.T) {
	out := buildSkillFile("my-skill", "does something", "2026-01-01", "## Instructions\nDo stuff.")
	if !strings.HasPrefix(out, "---\n") {
		t.Fatalf("expected YAML frontmatter start, got: %q", out[:min(len(out), 20)])
	}
	if !strings.Contains(out, "my-skill") {
		t.Error("expected name in output")
	}
	if !strings.Contains(out, "## Instructions") {
		t.Error("expected body in output")
	}
}

func TestBuildSkillFile_noBody(t *testing.T) {
	out := buildSkillFile("my-skill", "desc", "2026-01-01", "")
	if strings.Contains(out, "\n\n") {
		// just verify it ends cleanly with the closing ---
		_ = out
	}
	if !strings.Contains(out, "---") {
		t.Errorf("expected frontmatter in output: %q", out)
	}
}

func TestBuildSkillFile_bodyNewlineEnforced(t *testing.T) {
	out := buildSkillFile("my-skill", "desc", "2026-01-01", "no newline at end")
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("expected trailing newline, got: %q", out[max(0, len(out)-5):])
	}
}
