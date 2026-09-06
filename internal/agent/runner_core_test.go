package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	systemplugins "github.com/CherryHQ/stella/plugins/system"
)

// fixtureRunnerSystemRuntimePlan creates the complete startup-shaped core plan
// without running the installer or downloading tools. Runner tests use this
// same fixture so the none backend exercises the same plan validation as the
// native startup path.
func fixtureRunnerSystemRuntimePlan(t *testing.T, root string) *systemplugins.RuntimePlan {
	t.Helper()
	identity, err := systemplugins.RuntimeIdentity()
	if err != nil {
		t.Fatalf("systemplugins.RuntimeIdentity: %v", err)
	}
	publicDir := filepath.Join(root, "core-runtime")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("create core fixture directory: %v", err)
	}
	plan := &systemplugins.RuntimePlan{
		Identity:     identity,
		PublicDir:    publicDir,
		PublicBinDir: publicDir,
		Runtimes:     make([]systemplugins.Runtime, 0, len(systemplugins.RuntimeResources())),
	}
	for _, resource := range systemplugins.RuntimeResources() {
		name := resource.Name
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(publicDir, name)
		if err := os.WriteFile(path, []byte("fixture runtime\n"), 0o755); err != nil {
			t.Fatalf("write core fixture %s: %v", resource.Name, err)
		}
		plan.Runtimes = append(plan.Runtimes, systemplugins.Runtime{
			Name: resource.Name, Version: resource.Version, Path: path, Available: true,
		})
	}
	if err := systemplugins.Verify(*plan); err != nil {
		t.Fatalf("systemplugins.Verify fixture: %v", err)
	}
	return plan
}
