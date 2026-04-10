package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Create creates a new skill with the given name, description, and content body.
// The skill is created with status=draft and created-at=now.
// targetDir must be the writable skills directory (userSkillsDir or workspace/skills).
func Create(name, description, content, targetDir string) error {
	if errs := validateCreateInput(name, description); len(errs) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}

	skillDir := filepath.Join(targetDir, name)
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(skillFile); err == nil {
		return fmt.Errorf("skill %q already exists at %s", name, skillFile)
	}

	data := buildSkillFile(name, description, SkillStatusDraft, time.Now().UTC().Format(time.RFC3339), content)

	skillWriteMu.Lock()
	defer skillWriteMu.Unlock()

	return atomicWriteFile(skillFile, []byte(data), 0o644)
}

// Patch updates frontmatter fields and/or the content body of an existing skill.
// Supported update keys: "description", "status", "content" (body after frontmatter).
func Patch(name string, updates map[string]string, targetDir string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !safeNameRe.MatchString(name) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	if len(updates) == 0 {
		return fmt.Errorf("no updates provided")
	}

	skillFile := filepath.Join(targetDir, name, "SKILL.md")
	existing, err := os.ReadFile(skillFile)
	if err != nil {
		return fmt.Errorf("skill %q not found at %s: %w", name, skillFile, err)
	}

	fm, body, err := splitFrontmatterAndBody(string(existing))
	if err != nil {
		return fmt.Errorf("parse existing skill: %w", err)
	}

	if v, ok := updates["description"]; ok {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("description must not be empty")
		}
		fm["description"] = v
	}
	if v, ok := updates["status"]; ok {
		fm["status"] = NormalizeSkillStatus(v)
	}
	if v, ok := updates["content"]; ok {
		body = v
	}

	data := renderSkillFile(fm, body)

	skillWriteMu.Lock()
	defer skillWriteMu.Unlock()

	return atomicWriteFile(skillFile, []byte(data), 0o644)
}

// Deprecate sets the status of an existing skill to "deprecated".
func Deprecate(name, targetDir string) error {
	return Patch(name, map[string]string{"status": SkillStatusDeprecated}, targetDir)
}

func validateCreateInput(name, description string) []string {
	var errs []string
	if name == "" {
		errs = append(errs, "name is required")
	} else if !safeNameRe.MatchString(name) {
		errs = append(errs, fmt.Sprintf("invalid skill name %q: must match %s", name, safeNameRe.String()))
	}
	if strings.TrimSpace(description) == "" {
		errs = append(errs, "description is required")
	}
	return errs
}

func buildSkillFile(name, description, status, createdAt, body string) string {
	fm := map[string]string{
		"name":        name,
		"description": description,
		"status":      status,
		"created-at":  createdAt,
	}
	return renderSkillFile(fm, body)
}

func renderSkillFile(fm map[string]string, body string) string {
	keys := make([]string, 0, len(fm))
	for k := range fm {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range keys {
		line, _ := yaml.Marshal(map[string]string{k: fm[k]})
		b.Write(line)
	}
	b.WriteString("---\n")
	if body != "" {
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func splitFrontmatterAndBody(content string) (map[string]string, string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	if !strings.HasPrefix(content, "---") {
		return nil, "", fmt.Errorf("no frontmatter")
	}

	endIdx := strings.Index(content[3:], "\n---")
	if endIdx == -1 {
		return nil, "", fmt.Errorf("no closing frontmatter delimiter")
	}

	yamlStr := content[4 : 3+endIdx]
	body := ""
	afterFM := 3 + endIdx + 4
	if afterFM < len(content) {
		body = content[afterFM:]
	}

	fm := make(map[string]string)
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return nil, "", fmt.Errorf("invalid yaml: %w", err)
	}

	return fm, body, nil
}
