package embedded

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/plugins"
)

// EnsurePlugins extracts all embedded plugin manifests to annaHome/plugins/bundled/.
// Each manifest is written to {kind}/{name}/plugin.json.
// Unlike EnsureTools, this always overwrites since JSON manifests are small and
// the destination may change (e.g. different ANNA_HOME in tests).
func EnsurePlugins(annaHome string) error {
	return extractPlugins(filepath.Join(annaHome, "plugins", "bundled"))
}

func extractPlugins(destDir string) error {
	return fs.WalkDir(plugins.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		destPath := filepath.Join(destDir, path)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", path, err)
		}

		data, err := plugins.FS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}

		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}

		return nil
	})
}
