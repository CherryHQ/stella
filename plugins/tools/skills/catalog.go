package skills

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
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

// LoadSkillsConfig controls skill discovery roots.
type LoadSkillsConfig struct {
	Runtime       pkgplugins.ToolRuntime
	AnnaHome      string
	AgentRoot     string
	UserRoot      string
	ProjectRoot   string
	UserSkillsDir string
}

type skillFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	Status                 string `yaml:"status"`
	CreatedAt              string `yaml:"created-at"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

const maxNameLength = 64

var validNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// LoadSkills discovers skills in increasing priority order:
// builtin -> ANNA_HOME -> agent root -> user root -> project root.
func LoadSkills(ctx context.Context, cfg LoadSkillsConfig) []Skill {
	return loadSkills(ctx, cfg.Runtime, cfg.AnnaHome, cfg.AgentRoot, cfg.UserRoot, cfg.ProjectRoot, cfg.UserSkillsDir)
}

func loadSkills(ctx context.Context, runtime pkgplugins.ToolRuntime, annaHome, agentRoot, userRoot, projectRoot, userSkillsDir string) []Skill {
	indexByName := map[string]int{}
	var skills []Skill

	add := func(s Skill) {
		if idx, ok := indexByName[s.Name]; ok {
			skills[idx] = s
			return
		}
		indexByName[s.Name] = len(skills)
		skills = append(skills, s)
	}

	dedupPaths := map[string]bool{}
	addDir := func(dir, source string) {
		abs, _ := filepath.Abs(dir)
		if dedupPaths[abs] {
			return
		}
		dedupPaths[abs] = true
		for _, s := range loadSkillsFromDir(ctx, runtime, dir, source) {
			add(s)
		}
	}

	if annaHome != "" {
		// Always load from built-in skills dir.
		addDir(filepath.Join(annaHome, "skills"), "anna")
	}
	if agentRoot != "" {
		addDir(filepath.Join(agentRoot, ".agents", "skills"), "agent")
	}
	if userSkillsDir == "" && userRoot != "" {
		userSkillsDir = filepath.Join(userRoot, ".agents", "skills")
	}
	if userSkillsDir != "" {
		addDir(userSkillsDir, "user")
	}
	if projectRoot != "" {
		addDir(filepath.Join(projectRoot, ".agents", "skills"), "project")
	}

	return skills
}

func loadSkillsFromDir(ctx context.Context, runtime pkgplugins.ToolRuntime, dir, source string) []Skill {
	info, err := statSkillPath(ctx, runtime, dir)
	if err != nil || !info.Exists || !info.IsDir {
		return nil
	}
	return scanDir(ctx, runtime, dir, source, true)
}

func scanDir(ctx context.Context, runtime pkgplugins.ToolRuntime, dir, source string, isRoot bool) []Skill {
	entries, err := readSkillDir(ctx, runtime, dir)
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
			skills = append(skills, scanDir(ctx, runtime, fullPath, source, false)...)
			continue
		}
		if isRoot && name == "SKILL.md" {
			continue
		}
		if name != "SKILL.md" || !entryIsRegular(ctx, runtime, fullPath) {
			continue
		}

		if s, ok := loadSkillFromFile(ctx, runtime, fullPath, source); ok {
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

func loadSkillFromFile(ctx context.Context, runtime pkgplugins.ToolRuntime, filePath, source string) (Skill, bool) {
	data, err := readSkillFile(ctx, runtime, filePath)
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
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return Skill{}, false
	}
	if errs := ValidateSkillName(name, filepath.Base(skillDir)); len(errs) > 0 {
		return Skill{}, false
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
	if strings.TrimSpace(name) == "" {
		errs = append(errs, "name is required")
		return errs
	}
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

func skillNameValidationError(name, parentDirName string) error {
	errs := ValidateSkillName(name, parentDirName)
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 && errs[0] == "name is required" {
		return fmt.Errorf("name is required")
	}
	return fmt.Errorf("invalid skill name %q: %s", name, strings.Join(errs, "; "))
}
