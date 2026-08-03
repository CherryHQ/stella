package skills

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// ListProjectSkills walks {root}/.agents/skills/ and returns skill metadata structs
// with Scope="project". The second return value maps skill name → skill directory path
// so callers can read file content off disk for a given project skill.
func ListProjectSkills(root string) ([]pkgplugins.Skill, map[string]string, error) {
	return listFSSkills(root, "project")
}

func listFSSkills(root, scope string) ([]pkgplugins.Skill, map[string]string, error) {
	if root == "" {
		return nil, nil, nil
	}

	skillsDir := filepath.Join(root, ".agents", "skills")
	if _, err := os.Stat(skillsDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var out []pkgplugins.Skill
	dirs := make(map[string]string)

	err := filepath.WalkDir(skillsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || path == skillsDir {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			return filepath.SkipDir
		}

		skillMD := filepath.Join(path, pkgplugins.SkillMainFile)
		data, err := os.ReadFile(skillMD)
		if err != nil {
			return nil //nolint:nilerr // no SKILL.md — recurse into subdirectories
		}

		fm, parseErr := parseFrontmatter(string(data))
		if parseErr != nil {
			slog.Warn("fs_skill: cannot parse frontmatter", "scope", scope, "path", skillMD, "err", parseErr)
			return filepath.SkipDir
		}

		skillName := strings.TrimSpace(fm.Name)
		if skillName == "" {
			skillName = name
		}
		if strings.TrimSpace(fm.Description) == "" {
			return filepath.SkipDir
		}
		if skillName != name {
			slog.Warn("fs_skill: skill name does not match directory, skipping",
				"scope", scope, "name", skillName, "dir", name)
			return filepath.SkipDir
		}

		out = append(out, pkgplugins.Skill{
			ID:                     scope + ":" + path,
			Scope:                  scope,
			Name:                   skillName,
			Description:            fm.Description,
			Status:                 SkillStatusActive,
			DisableModelInvocation: fm.DisableModelInvocation,
			CreatedAt:              time.Time{},
		})
		dirs[skillName] = path
		return filepath.SkipDir
	})
	if err != nil {
		return nil, nil, err
	}

	return out, dirs, nil
}

// loadProjectSkillFile reads a file from a project skill directory.
// skillDir is the absolute path to the skill directory.
func loadProjectSkillFile(skillDir, path string) (string, error) {
	fullPath := filepath.Join(skillDir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
