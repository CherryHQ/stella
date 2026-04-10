package builtin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed anna agents
var skillsFS embed.FS

// Extract writes all embedded builtin skill files to destDir.
func Extract(destDir string) error {
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("clean builtin skills dir: %w", err)
	}

	return fs.WalkDir(skillsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := skillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
