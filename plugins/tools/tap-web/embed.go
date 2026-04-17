package tapweb

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed skill/tap-web
var skillFS embed.FS

// ExtractSkill writes the tap-web skill into skillsDir/tap-web/.
// Existing content is replaced atomically to keep the skill in sync with the binary.
func ExtractSkill(skillsDir string) error {
	dst := filepath.Join(skillsDir, "tap-web")
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clean tap-web skill: %w", err)
	}
	return fs.WalkDir(skillFS, "skill/tap-web", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Strip the "skill/" prefix so the target is skillsDir/tap-web/...
		rel, _ := filepath.Rel("skill", path)
		target := filepath.Join(skillsDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
