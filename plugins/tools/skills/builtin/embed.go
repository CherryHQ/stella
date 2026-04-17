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

// ExtractSkills writes the builtin anna skill into skillsDir.
// Only the anna/ subdirectory is replaced; other content in skillsDir is preserved.
func ExtractSkills(skillsDir string) error {
	annaDir := filepath.Join(skillsDir, "anna")
	if err := os.RemoveAll(annaDir); err != nil {
		return fmt.Errorf("clean builtin anna skill: %w", err)
	}
	return fs.WalkDir(skillsFS, "anna", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(skillsDir, path)
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
