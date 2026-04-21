package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMigratedSandboxPathsAvoidDirectBypasses(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"os/exec",
		"net/http",
		"exec.Command",
		"http.NewRequest",
		"http.Client{",
		"os.ReadFile",
		"os.ReadDir",
		"os.Stat",
	}
	files := []string{
		"plugins/tools/skills/plugin.go",
		"plugins/tools/skills/tool.go",
		"plugins/tools/skills/catalog.go",
		"plugins/tools/skills/manage.go",
		"plugins/tools/agent/preset_loader.go",
		"plugins/tools/agent/hostfs.go",
		"internal/agent/runner/prompt.go",
		// prompt_host.go intentionally uses os.* for the non-runner fallback path
		// (no sandbox session available during standalone prompt rendering).
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join(root, filepath.Clean(path)))
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			text := string(data)
			for _, needle := range forbidden {
				if strings.Contains(text, needle) {
					t.Fatalf("%s contains forbidden bypass marker %q", path, needle)
				}
			}
		})
	}
}

func TestPluginPackagesDoNotImportInternalSandbox(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, dir := range []string{"plugins", "pkg/plugins"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if strings.Contains(string(data), `"github.com/vaayne/anna/internal/sandbox"`) {
					t.Fatalf("%s imports internal sandbox", path)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", dir, err)
			}
		})
	}
}
