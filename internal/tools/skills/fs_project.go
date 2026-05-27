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

// ListSystemSkills walks {stellaHome}/.agents/skills/ and returns skill metadata
// structs with Scope="system". System skills are extracted from the embedded FS
// at startup and live on disk — they are never stored in the DB.
func ListSystemSkills(stellaHome string) ([]pkgplugins.Skill, map[string]string, error) {
	return listFSSkills(stellaHome, "system")
}

func listFSSkills(root, scope string) ([]pkgplugins.Skill, map[string]string, error) {
	if root == "" {
		return nil, nil, nil
	}

	skillsDir := filepath.Join(root, ".agents", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var skills []pkgplugins.Skill
	dirs := make(map[string]string)

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(skillsDir, name)
		skillMD := filepath.Join(skillDir, pkgplugins.SkillMainFile)

		data, err := os.ReadFile(skillMD)
		if err != nil {
			continue // no SKILL.md
		}

		fm, err := parseFrontmatter(string(data))
		if err != nil {
			slog.Warn("fs_skill: cannot parse frontmatter", "scope", scope, "path", skillMD, "err", err)
			continue
		}

		skillName := strings.TrimSpace(fm.Name)
		if skillName == "" {
			skillName = name
		}
		if strings.TrimSpace(fm.Description) == "" {
			continue
		}
		if skillName != name {
			slog.Warn("fs_skill: skill name does not match directory, skipping",
				"scope", scope, "name", skillName, "dir", name)
			continue
		}

		sk := pkgplugins.Skill{
			ID:                     scope + ":" + skillDir,
			Scope:                  scope,
			Name:                   skillName,
			Description:            fm.Description,
			Status:                 NormalizeSkillStatus(fm.Status),
			DisableModelInvocation: fm.DisableModelInvocation,
			CreatedAt:              time.Time{},
		}
		skills = append(skills, sk)
		dirs[skillName] = skillDir
	}

	return skills, dirs, nil
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
