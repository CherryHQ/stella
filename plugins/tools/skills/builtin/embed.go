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

// ExtractSkills writes the builtin anna skill into skillsDir.
// Only the anna/ subdirectory is replaced; other content in skillsDir is preserved.
// Result: skillsDir/anna/SKILL.md, skillsDir/anna/references/*.md
func ExtractSkills(skillsDir string) error {
	if err := os.RemoveAll(filepath.Join(skillsDir, "anna")); err != nil {
		return fmt.Errorf("clean builtin anna skill: %w", err)
	}
	return fs.WalkDir(skillsFS, "anna", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(skillsDir, path) // preserves anna/ prefix
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

// ExtractAgents writes builtin agent preset files directly into agentsDir.
// Individual files are overwritten; other content in agentsDir is preserved.
// Result: agentsDir/coder.md, agentsDir/researcher.md, etc.
func ExtractAgents(agentsDir string) error {
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("create agents dir: %w", err)
	}
	return fs.WalkDir(skillsFS, "agents", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil // agentsDir is already flat
		}
		data, err := skillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(agentsDir, filepath.Base(path)), data, 0o644)
	})
}
