package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	pluginBinaryOnce sync.Once
	pluginBinaryPath string
	pluginBinaryErr  error
)

func TestMain(m *testing.M) {
	os.Exit(runTestsWithPluginBinary(m, filepath.Join("..", "..")))
}

func runTestsWithPluginBinary(m *testing.M, rootRel string) int {
	pluginBinaryOnce.Do(func() {
		root, err := filepath.Abs(rootRel)
		if err != nil {
			pluginBinaryErr = err
			return
		}
		dir, err := os.MkdirTemp("", "anna-plugin-bin-")
		if err != nil {
			pluginBinaryErr = err
			return
		}
		binPath := filepath.Join(dir, "anna-plugin-test-bin")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/anna-plugin")
		cmd.Dir = root
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			pluginBinaryErr = fmt.Errorf("build anna binary: %w: %s", err, string(out))
			return
		}
		pluginBinaryPath = binPath
	})
	if pluginBinaryErr != nil {
		fmt.Fprintln(os.Stderr, pluginBinaryErr)
		return 1
	}
	if err := os.Setenv("ANNA_PLUGIN_ENTRYPOINT", pluginBinaryPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
