package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	prompt := BuildSystemPromptFromDB(context.Background(), DBPromptParams{
		SystemPrompt: "You are Anna.",
		Cwd:          project,
	})
	if !strings.Contains(prompt, "root instructions") {
		t.Fatalf("expected root AGENTS.md content in prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "project instructions") {
		t.Fatalf("expected project AGENTS.md content in prompt: %s", prompt)
	}
}
