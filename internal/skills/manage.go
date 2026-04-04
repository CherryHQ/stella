package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
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

	// Refuse to overwrite an existing skill.
	if _, err := os.Stat(skillFile); err == nil {
		return fmt.Errorf("skill %q already exists at %s", name, skillFile)
	}

	data := buildSkillFile(name, description, runner.SkillStatusDraft, time.Now().UTC().Format(time.RFC3339), content)

	skillWriteMu.Lock()
	defer skillWriteMu.Unlock()

	return AtomicWriteFile(skillFile, []byte(data), 0o644)
}

// Patch updates frontmatter fields and/or the content body of an existing skill.
// Supported update keys: "description", "status", "content" (body after frontmatter).
// targetDir must be the writable skills directory.
func Patch(name string, updates map[string]string, targetDir string) error {
	if name == "" {
		return fmt.Errorf("name is required")
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
		normalized := runner.NormalizeSkillStatus(v)
		fm["status"] = normalized
	}
	if v, ok := updates["content"]; ok {
		body = v
	}

	data := renderSkillFile(fm, body)

	skillWriteMu.Lock()
	defer skillWriteMu.Unlock()

	return AtomicWriteFile(skillFile, []byte(data), 0o644)
}

// Deprecate sets the status of an existing skill to "deprecated".
// targetDir must be the writable skills directory.
func Deprecate(name, targetDir string) error {
	return Patch(name, map[string]string{"status": runner.SkillStatusDeprecated}, targetDir)
}

// validateCreateInput checks required fields for skill creation.
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

// buildSkillFile renders a complete SKILL.md with frontmatter and body.
func buildSkillFile(name, description, status, createdAt, body string) string {
	fm := map[string]string{
		"name":        name,
		"description": description,
		"status":      status,
		"created-at":  createdAt,
	}
	return renderSkillFile(fm, body)
}

// renderSkillFile renders a SKILL.md from a frontmatter map and body text.
func renderSkillFile(fm map[string]string, body string) string {
	yamlBytes, _ := yaml.Marshal(fm)
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(yamlBytes)
	b.WriteString("---\n")
	if body != "" {
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// splitFrontmatterAndBody splits a SKILL.md into its frontmatter fields and body.
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
	afterFM := 3 + endIdx + 4 // skip past "\n---\n"
	if afterFM < len(content) {
		body = content[afterFM:]
	}

	fm := make(map[string]string)
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return nil, "", fmt.Errorf("invalid yaml: %w", err)
	}

	return fm, body, nil
}
