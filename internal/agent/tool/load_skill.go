package tool

import (
	"context"
	"fmt"
	"os"

	"github.com/vaayne/anna/internal/toolspec"
)

// SkillEntry holds the metadata for a discovered skill.
type SkillEntry struct {
	Name    string
	BaseDir string
	Path    string // absolute path to the SKILL.md or .md file
}

// LoadSkillTool reads a skill file by name and returns its content and base directory.
type LoadSkillTool struct {
	skills map[string]SkillEntry
}

// NewLoadSkillTool creates a LoadSkillTool from the given skill entries.
func NewLoadSkillTool(skills []SkillEntry) *LoadSkillTool {
	m := make(map[string]SkillEntry, len(skills))
	for _, s := range skills {
		m[s.Name] = s
	}
	return &LoadSkillTool{skills: m}
}

func (t *LoadSkillTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        "load_skill",
		Description: "Load a skill by name. Returns the skill's full content and base directory for resolving relative paths.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The skill name to load.",
				},
			},
			"required": []string{"name"},
		},
	}
}

func (t *LoadSkillTool) Execute(_ context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("load_skill: name is required")
	}

	entry, ok := t.skills[name]
	if !ok {
		return "", fmt.Errorf("load_skill: unknown skill %q", name)
	}

	data, err := os.ReadFile(entry.Path)
	if err != nil {
		return "", fmt.Errorf("load_skill: %w", err)
	}

	return fmt.Sprintf("<skill_content name=%q base_dir=%q>\n%s\n</skill_content>", entry.Name, entry.BaseDir, string(data)), nil
}
