package embedded

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

var ensurePluginsOnce sync.Once

// EnsurePlugins extracts all embedded plugin manifests to annaHome/plugins/bundled/.
// Each manifest is written to {kind}/{name}/plugin.json. Safe for concurrent calls.
func EnsurePlugins(annaHome string) error {
	var err error
	ensurePluginsOnce.Do(func() {
		err = extractPlugins(filepath.Join(annaHome, "plugins", "bundled"))
	})
	return err
}

func extractPlugins(destDir string) error {
	return fs.WalkDir(pluginsFS, pluginsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		// path is like "plugins/tool/read/plugin.json"
		// We want the relative part after "plugins/" → "tool/read/plugin.json"
		rel, err := filepath.Rel(pluginsDir, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}

		destPath := filepath.Join(destDir, rel)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", rel, err)
		}

		data, err := pluginsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}

		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}

		return nil
	})
}
