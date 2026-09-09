package skill

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	SkillStatusActive     = "active"
	SkillStatusDeprecated = "deprecated"
)

type skillFrontmatter struct {
	Name                   string         `yaml:"name"`
	Description            string         `yaml:"description"`
	Status                 string         `yaml:"status"`
	CreatedAt              string         `yaml:"created-at"`
	DisableModelInvocation bool           `yaml:"disable-model-invocation"`
	Metadata               map[string]any `yaml:"metadata"`
}

const maxNameLength = 64

var validNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func parseFrontmatter(content string) (skillFrontmatter, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	if !strings.HasPrefix(content, "---") {
		return skillFrontmatter{}, fmt.Errorf("no frontmatter")
	}

	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return skillFrontmatter{}, fmt.Errorf("invalid frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return skillFrontmatter{}, fmt.Errorf("no closing frontmatter delimiter")
	}
	yamlStr := strings.Join(lines[1:end], "\n")

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return skillFrontmatter{}, fmt.Errorf("invalid yaml: %w", err)
	}
	if fm.Metadata == nil {
		fm.Metadata = map[string]any{}
	}
	delete(fm.Metadata, "created_by")
	if fm.Status == "" {
		fm.Status = SkillStatusActive
	}
	if fm.Status != SkillStatusActive && fm.Status != SkillStatusDeprecated {
		return skillFrontmatter{}, fmt.Errorf("invalid skill status %q", fm.Status)
	}

	return fm, nil
}

func skillMetadataJSON(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte(`{}`), nil
	}
	clean := maps.Clone(metadata)
	delete(clean, "created_by")
	return json.Marshal(clean)
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
