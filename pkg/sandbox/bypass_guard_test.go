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
		"internal/skill/plugin.go",
		"internal/skill/tool.go",
		"internal/skill/catalog.go",
		"internal/skill/manage.go",
		"internal/agent/delegate/preset_loader.go",
		"internal/agent/prompt/prompt.go",
		// prompt/host.go intentionally uses os.* for host-side operations (no
		// sandbox session available during prompt rendering, which runs in the
		// stella process before a session is created).
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
