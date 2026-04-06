package runner

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vaayne/anna/internal/agent/runner/builtin"
	"gopkg.in/yaml.v3"
)

// Skill status constants.
const (
	SkillStatusDraft      = "draft"
	SkillStatusActive     = "active"
	SkillStatusDeprecated = "deprecated"
)

// Skill represents a discovered skill with its metadata and location.
type Skill struct {
	Name                   string
	Description            string
	Status                 string // "draft", "active", or "deprecated"
	CreatedAt              string // RFC 3339 timestamp from frontmatter
	FilePath               string // absolute path to the SKILL.md or .md file
	BaseDir                string // directory containing the skill file
	Source                 string // "user", "project", or "path"
	DisableModelInvocation bool
}

// skillFrontmatter is the YAML frontmatter parsed from a SKILL.md file.
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
// annaHome is the anna home directory (e.g. ~/.anna), workspace is the agent workspace
// dir (e.g. ~/.anna/workspaces/{agentID}), cwd is the working directory, and
// userSkillsDir is the optional per-user skills directory.
// Priority order: cwd/.agents/skills/ > userSkillsDir > workspace/skills/ > ~/.agents/skills/ > builtin
func LoadSkills(annaHome, workspace, cwd string, userSkillsDir ...string) []Skill {
	home, _ := os.UserHomeDir()
	var usd string
	if len(userSkillsDir) > 0 {
		usd = userSkillsDir[0]
	}
	return loadSkills(home, annaHome, workspace, cwd, usd)
}

func loadSkills(homeDir, annaHome, workspace, cwd, userSkillsDir string) []Skill {
	seen := map[string]bool{} // name → already loaded
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
		for _, s := range loadSkillsFromDir(dir, source) {
			add(s)
		}
	}

	// 1. Project-local skills: cwd/.agents/skills/ (highest priority)
	if cwd != "" {
		addDir(filepath.Join(cwd, ".agents", "skills"), "project")
	}

	// 2. User-specific skills: per-user installed skills (when userSkillsDir is set)
	if userSkillsDir != "" {
		addDir(userSkillsDir, "user")
	}

	// 3. Agent-level workspace skills: workspace/skills/ (shared across all users)
	if workspace != "" {
		addDir(filepath.Join(workspace, "skills"), "agent")
	}

	// 4. Common skills: ~/.agents/skills/ (legacy/shared)
	if homeDir != "" {
		addDir(filepath.Join(homeDir, ".agents", "skills"), "common")
	}

	// 5. Builtin skills: extracted from binary (lowest priority)
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

// loadSkillsFromDir scans a directory for skills.
// Discovery rules (matching Pi):
//   - Direct .md files in the root directory
//   - Recursive SKILL.md files under subdirectories
func loadSkillsFromDir(dir, source string) []Skill {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}

	return scanDir(dir, source, true)
}

func scanDir(dir, source string, isRoot bool) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var skills []Skill

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}

		fullPath := filepath.Join(dir, name)

		if entry.IsDir() {
			// Recurse into subdirectories looking for SKILL.md
			skills = append(skills, scanDir(fullPath, source, false)...)
			continue
		}

		if !entry.Type().IsRegular() {
			continue
		}

		// In root: any .md file. In subdirs: only SKILL.md.
		isRootMd := isRoot && strings.HasSuffix(name, ".md")
		isSkillMd := !isRoot && name == "SKILL.md"

		if !isRootMd && !isSkillMd {
			continue
		}

		if s, ok := loadSkillFromFile(fullPath, source); ok {
			skills = append(skills, s)
		}
	}

	return skills
}

// NormalizeSkillStatus normalizes an empty status to "active" and validates
// known values. Returns the normalized status or "active" if the value is unknown.
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

func loadSkillFromFile(filePath, source string) (Skill, bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Skill{}, false
	}

	fm, err := parseFrontmatter(string(data))
	if err != nil {
		return Skill{}, false
	}

	// Description is required — skip skills without one.
	if strings.TrimSpace(fm.Description) == "" {
		return Skill{}, false
	}

	skillDir := filepath.Dir(filePath)
	parentDirName := filepath.Base(skillDir)

	// Name: from frontmatter, or fall back to parent directory name.
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

// parseFrontmatter extracts YAML frontmatter delimited by "---" from content.
func parseFrontmatter(content string) (skillFrontmatter, error) {
	// Normalize line endings.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	if !strings.HasPrefix(content, "---") {
		return skillFrontmatter{}, fmt.Errorf("no frontmatter")
	}

	endIdx := strings.Index(content[3:], "\n---")
	if endIdx == -1 {
		return skillFrontmatter{}, fmt.Errorf("no closing frontmatter delimiter")
	}

	yamlStr := content[4 : 3+endIdx] // skip "---\n", end before "\n---"

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return skillFrontmatter{}, fmt.Errorf("invalid yaml: %w", err)
	}

	return fm, nil
}

// VisibleSkills filters skills for prompt rendering: excludes DisableModelInvocation
// and deprecated skills.
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
// Returns validation errors (empty slice if valid).
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

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
