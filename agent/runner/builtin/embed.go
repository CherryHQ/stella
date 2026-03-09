package builtin

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed anna
var skillsFS embed.FS

// Extract writes all embedded builtin skill files to destDir, overwriting
// any existing files. This ensures the on-disk copy always matches the binary.
// Returns the destDir path for use as a skill scan directory.
func Extract(destDir string) error {
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
