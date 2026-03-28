package runner

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
	pluginBinaryOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", "..", ".."))
		if err != nil {
			pluginBinaryErr = err
			return
		}
		dir, err := os.MkdirTemp("", "anna-plugin-bin-")
		if err != nil {
			pluginBinaryErr = err
			return
		}
		binPath := filepath.Join(dir, "anna-test-bin")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/anna")
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
		os.Exit(1)
	}
	if err := os.Setenv("ANNA_PLUGIN_ENTRYPOINT", pluginBinaryPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
