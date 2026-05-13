package prompt_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
)

func TestBuildSystemPromptLoadsProjectContextWithoutInjectedHost(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	project := filepath.Join(root, "project", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		ProjectRoot:  project,
	})
	if !strings.Contains(p, "root instructions") {
		t.Fatalf("expected root AGENTS.md content in prompt: %s", p)
	}
	if !strings.Contains(p, "project instructions") {
		t.Fatalf("expected project AGENTS.md content in prompt: %s", p)
	}
}
