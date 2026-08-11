package prompt_test

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/home"
)

type promptMapRoot struct{ files fstest.MapFS }

func (promptMapRoot) Close() error { return nil }

func (r promptMapRoot) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return fs.Stat(r.files, name)
}

func (r promptMapRoot) Read(_ context.Context, name string, dst io.Writer, options home.ReadOptions) error {
	data, err := fs.ReadFile(r.files, name)
	if err != nil {
		return err
	}
	if int64(len(data)) > options.MaxBytes {
		return home.ErrReadLimit
	}
	_, err = dst.Write(data)
	return err
}

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

func TestBuildSystemPromptLoadsBoundedContextThroughAuthorizedRoot(t *testing.T) {
	root := promptMapRoot{files: fstest.MapFS{
		"AGENTS.md":             {Data: []byte("root capability instructions")},
		"project/AGENTS.md":     {Data: []byte("project capability instructions")},
		"project/app/AGENTS.md": {Data: []byte("app capability instructions")},
		"outside/AGENTS.md":     {Data: []byte("must not appear")},
	}}

	projectContext, err := prompt.SnapshotProjectContext(context.Background(), root, "project/app")
	if err != nil {
		t.Fatal(err)
	}
	result := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", ProjectContext: projectContext})
	for _, want := range []string{"root capability instructions", "project capability instructions", "app capability instructions"} {
		if !strings.Contains(result, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	if strings.Contains(result, "must not appear") {
		t.Fatal("prompt crossed the authorized project ancestry")
	}

	root.files["project/app/AGENTS.md"] = &fstest.MapFile{Data: []byte(strings.Repeat("x", 256*1024+1))}
	projectContext, err = prompt.SnapshotProjectContext(context.Background(), root, "project/app")
	if err != nil {
		t.Fatal(err)
	}
	result = prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{SystemPrompt: "You are Stella.", ProjectContext: projectContext})
	if strings.Contains(result, strings.Repeat("x", 128)) {
		t.Fatal("oversized context was included")
	}
}
