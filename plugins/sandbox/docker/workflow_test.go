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

// TestSandboxImageBuildsPassVersion asserts every build path (CI workflow and
// the local mise task) forwards a VERSION build arg, so the fs-helper-revision
// label the Dockerfile derives from it is populated coherently everywhere.
func TestSandboxImageBuildsPassVersion(t *testing.T) {
	workflowPath := filepath.Join("..", "..", "..", ".github", "workflows", "sandbox-docker.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read sandbox image workflow: %v", err)
	}
	if !strings.Contains(string(workflow), "VERSION=${{") {
		t.Error("sandbox image workflow must pass a VERSION build arg")
	}

	misePath := filepath.Join("..", "..", "..", "mise.toml")
	mise, err := os.ReadFile(misePath)
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	if !strings.Contains(string(mise), "--build-arg VERSION=$VERSION_ARG") {
		t.Error("sandbox:docker:build task must pass a VERSION build arg")
	}
}
