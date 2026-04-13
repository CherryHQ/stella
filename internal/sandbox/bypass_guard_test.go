package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPhase5MigratedPathsAvoidDirectBypasses(t *testing.T) {
	t.Parallel()

	forbidden := []string{"os/exec", "net/http", "exec.Command", "http.NewRequest", "http.Client{"}
	files := []string{
		"plugins/tools/skills/plugin.go",
		"plugins/tools/skills/tool.go",
		"plugins/tools/skills/catalog.go",
		"plugins/tools/skills/manage.go",
		"plugins/tools/skills/remove_lib.go",
		"plugins/tools/agent/preset_loader.go",
		"internal/agent/runner/prompt.go",
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
