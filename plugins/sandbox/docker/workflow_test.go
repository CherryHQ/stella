package docker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxImageWorkflowPassesBuiltinBundleRevision(t *testing.T) {
	workflowPath := filepath.Join("..", "..", "..", ".github", "workflows", "sandbox-docker.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read sandbox image workflow: %v", err)
	}

	workflow := string(data)
	for _, want := range []string{
		`- "resources/**"`,
		`id: builtin-bundle`,
		`revision=$(go run ./cmd/stellad system-bundle revision)`,
		`BUILTIN_BUNDLE_REVISION=${{ steps.builtin-bundle.outputs.revision }}`,
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("sandbox image workflow does not contain %q", want)
		}
	}
}
