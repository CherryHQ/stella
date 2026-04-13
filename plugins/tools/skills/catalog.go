package skills

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/plugins/tools/skills/builtin"
	"gopkg.in/yaml.v3"
)

const (
	SkillStatusDraft      = "draft"
	SkillStatusActive     = "active"
	SkillStatusDeprecated = "deprecated"
)

type Skill struct {
	Name                   string
	Description            string
	Status                 string
	CreatedAt              string
	FilePath               string
	BaseDir                string
	Source                 string
	DisableModelInvocation bool
}

type skillFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	Status                 string `yaml:"status"`
	CreatedAt              string `yaml:"created-at"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

const (
	maxNameLength        = 64
	maxDescriptionLength = 1024
)

var validNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// LoadSkills discovers skills from project, user, workspace, and common directories.
func LoadSkills(ctx context.Context, host sandbox.Host, annaHome, workspace, cwd string, userSkillsDir ...string) []Skill {
	home, _ := os.UserHomeDir()
	var usd string
	if len(userSkillsDir) > 0 {
		usd = userSkillsDir[0]
	}
	return loadSkills(ctx, host, home, annaHome, workspace, cwd, usd)
}

func loadSkills(ctx context.Context, host sandbox.Host, homeDir, annaHome, workspace, cwd, userSkillsDir string) []Skill {
	seen := map[string]bool{}
	var skills []Skill

	add := func(s Skill) {
		if seen[s.Name] {
			return
		}
		seen[s.Name] = true
		skills = append(skills, s)
	}

	dedupPaths := map[string]bool{}
	addDir := func(dir, source string) {
		abs, _ := filepath.Abs(dir)
		if dedupPaths[abs] {
			return
		}
		dedupPaths[abs] = true
		for _, s := range loadSkillsFromDir(ctx, host, dir, source) {
			add(s)
		}
	}

	if cwd != "" {
		addDir(filepath.Join(cwd, ".agents", "skills"), "project")
	}
	if userSkillsDir != "" {
		addDir(userSkillsDir, "user")
	}
	if workspace != "" {
		addDir(filepath.Join(workspace, "skills"), "agent")
	}
	if homeDir != "" {
		addDir(filepath.Join(homeDir, ".agents", "skills"), "common")
	}
	if annaHome != "" {
		builtinDir := filepath.Join(annaHome, "cache", "builtin-skills")
		if err := builtin.Extract(builtinDir); err != nil {
			slog.Warn("failed to extract builtin skills", "error", err)
		} else {
			addDir(builtinDir, "builtin")
		}
	}

	return skills
}

func loadSkillsFromDir(ctx context.Context, host sandbox.Host, dir, source string) []Skill {
	info, err := statSkillPath(ctx, host, dir)
	if err != nil || !info.Exists || !info.IsDir {
		return nil
	}
	return scanDir(ctx, host, dir, source, true)
}

func scanDir(ctx context.Context, host sandbox.Host, dir, source string, isRoot bool) []Skill {
	entries, err := readSkillDir(ctx, host, dir)
	if err != nil {
		return nil
	}

	var skills []Skill
	for _, entry := range entries {
		name := entry.Name
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}

		fullPath := filepath.Join(dir, name)
		if entry.IsDir {
			skills = append(skills, scanDir(ctx, host, fullPath, source, false)...)
			continue
		}
		if !entry.IsDir && !entryIsRegular(ctx, host, fullPath) {
			continue
		}

		isRootMd := isRoot && strings.HasSuffix(name, ".md")
		isSkillMd := !isRoot && name == "SKILL.md"
		if !isRootMd && !isSkillMd {
			continue
		}
		if s, ok := loadSkillFromFile(ctx, host, fullPath, source); ok {
			skills = append(skills, s)
		}
	}

	return skills
}

func NormalizeSkillStatus(status string) string {
	normalized := strings.TrimSpace(strings.ToLower(status))
	switch normalized {
	case SkillStatusDraft:
		return SkillStatusDraft
	case SkillStatusActive, "":
		return SkillStatusActive
	case SkillStatusDeprecated:
		return SkillStatusDeprecated
	default:
		slog.Warn("unknown skill status, defaulting to active", "status", status)
		return SkillStatusActive
	}
}

func loadSkillFromFile(ctx context.Context, host sandbox.Host, filePath, source string) (Skill, bool) {
	data, err := readSkillFile(ctx, host, filePath)
	if err != nil {
		return Skill{}, false
	}

	fm, err := parseFrontmatter(string(data))
	if err != nil {
		return Skill{}, false
	}
	if strings.TrimSpace(fm.Description) == "" {
		return Skill{}, false
	}

	skillDir := filepath.Dir(filePath)
	parentDirName := filepath.Base(skillDir)
	name := fm.Name
	if name == "" {
		name = parentDirName
	}

	return Skill{
		Name:                   name,
		Description:            fm.Description,
		Status:                 NormalizeSkillStatus(fm.Status),
		CreatedAt:              fm.CreatedAt,
		FilePath:               filePath,
		BaseDir:                skillDir,
		Source:                 source,
		DisableModelInvocation: fm.DisableModelInvocation,
	}, true
}

func parseFrontmatter(content string) (skillFrontmatter, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	if !strings.HasPrefix(content, "---") {
		return skillFrontmatter{}, fmt.Errorf("no frontmatter")
	}

	endIdx := strings.Index(content[3:], "\n---")
	if endIdx == -1 {
		return skillFrontmatter{}, fmt.Errorf("no closing frontmatter delimiter")
	}

	yamlStr := content[4 : 3+endIdx]

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return skillFrontmatter{}, fmt.Errorf("invalid yaml: %w", err)
	}

	return fm, nil
}

func VisibleSkills(skills []Skill) []Skill {
	var visible []Skill
	for _, s := range skills {
		if !s.DisableModelInvocation && s.Status != SkillStatusDeprecated {
			visible = append(visible, s)
		}
	}
	return visible
}

// ValidateSkillName checks a skill name against the Agent Skills spec.
func ValidateSkillName(name, parentDirName string) []string {
	var errs []string
	if name != parentDirName {
		errs = append(errs, fmt.Sprintf("name %q does not match parent directory %q", name, parentDirName))
	}
	if len(name) > maxNameLength {
		errs = append(errs, fmt.Sprintf("name exceeds %d characters (%d)", maxNameLength, len(name)))
	}
	if !validNameRe.MatchString(name) {
		errs = append(errs, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errs = append(errs, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		errs = append(errs, "name must not contain consecutive hyphens")
	}
	return errs
}
