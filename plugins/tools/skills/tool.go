package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/pkg/tools"
)

var skillsInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["load", "search", "install", "list", "remove", "create", "patch", "deprecate"],
      "description": "Action to perform: 'load' reads a skill's content by name, 'search' finds skills from the ecosystem, 'install' adds a skill to the project, 'list' shows installed skills, 'remove' deletes an installed skill, 'create' creates a new skill (draft), 'patch' updates an existing skill's fields, 'deprecate' marks a skill as deprecated"
    },
    "query": {
      "type": "string",
      "description": "Search query (required for search)"
    },
    "limit": {
      "type": "integer",
      "description": "Max results to return (default 10, for search)"
    },
    "source": {
      "type": "string",
      "description": "Skill source to install. Supports: 'owner/repo@skill-name' (GitHub shorthand), 'owner/repo@skill-name#ref' (with branch/tag), GitHub/GitLab URLs, or local paths (required for install)"
    },
    "name": {
      "type": "string",
      "description": "Name of the skill (required for load, remove, create, patch, deprecate)"
    },
    "description": {
      "type": "string",
      "description": "Skill description (required for create, optional for patch)"
    },
    "content": {
      "type": "string",
      "description": "Skill body content in markdown (optional for create and patch)"
    },
    "status": {
      "type": "string",
      "enum": ["draft", "active", "deprecated"],
      "description": "Skill status (optional for patch)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

type Tool struct {
	annaHome      string
	workspace     string
	cwd           string
	userSkillsDir string
}

func NewTool(annaHome, workspace, cwd, userSkillsDir string) *Tool {
	return &Tool{
		annaHome:      annaHome,
		workspace:     workspace,
		cwd:           cwd,
		userSkillsDir: userSkillsDir,
	}
}

func (t *Tool) skillsDir() string {
	if t.userSkillsDir != "" {
		return t.userSkillsDir
	}
	return filepath.Join(t.workspace, "skills")
}

func pkgskillsToolDefinition() tools.Definition {
	return tools.Definition{
		Name:        "skills",
		Description: "Manage agent skills. Use 'load' to read a skill by name, 'search' to find skills from the ecosystem, 'install' to add a skill (e.g. owner/repo@skill-name), 'list' to see installed skills, 'remove' to delete one, 'create' to create a new skill (draft), 'patch' to update fields, 'deprecate' to mark as deprecated.",
		InputSchema: skillsInputSchema,
	}
}

func (t *Tool) Definition() tools.Definition {
	return pkgskillsToolDefinition()
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "load":
		return t.load(args)
	case "search":
		return t.search(ctx, args)
	case "install":
		return t.install(ctx, args)
	case "list":
		return t.list()
	case "remove":
		return t.remove(args)
	case "create":
		return t.create(args)
	case "patch":
		return t.patch(args)
	case "deprecate":
		return t.deprecate(args)
	default:
		return "", fmt.Errorf("unknown action %q, expected load/search/install/list/remove/create/patch/deprecate", action)
	}
}

func (t *Tool) create(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	description, _ := args["description"].(string)
	content, _ := args["content"].(string)

	targetDir := t.skillsDir()
	if err := Create(name, description, content, targetDir); err != nil {
		return "", err
	}

	return fmt.Sprintf("Skill %q created as draft in %s.", name, filepath.Join(targetDir, name)), nil
}

func (t *Tool) patch(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for patch action")
	}

	updates := make(map[string]string)
	if v, ok := args["description"].(string); ok && v != "" {
		updates["description"] = v
	}
	if v, ok := args["status"].(string); ok && v != "" {
		updates["status"] = v
	}
	if v, ok := args["content"].(string); ok && v != "" {
		updates["content"] = v
	}

	targetDir := t.skillsDir()
	if err := Patch(name, updates, targetDir); err != nil {
		return "", err
	}

	return fmt.Sprintf("Skill %q updated in %s.", name, filepath.Join(targetDir, name)), nil
}

func (t *Tool) deprecate(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for deprecate action")
	}

	targetDir := t.skillsDir()
	if err := Deprecate(name, targetDir); err != nil {
		return "", err
	}

	return fmt.Sprintf("Skill %q deprecated in %s.", name, filepath.Join(targetDir, name)), nil
}

type installedSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Removable   bool   `json:"removable"`
}

func (t *Tool) list() (string, error) {
	all := LoadSkills(t.annaHome, t.workspace, t.cwd, t.userSkillsDir)
	if len(all) == 0 {
		return "No skills installed.", nil
	}

	results := make([]installedSkill, len(all))
	for i, s := range all {
		results[i] = installedSkill{
			Name:        s.Name,
			Description: s.Description,
			Status:      s.Status,
			Source:      s.Source,
			Path:        s.FilePath,
			Removable:   s.Source == "project" || s.Source == "user",
		}
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	return string(out), nil
}

func (t *Tool) load(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for load action")
	}

	all := LoadSkills(t.annaHome, t.workspace, t.cwd, t.userSkillsDir)
	for _, s := range all {
		if s.Name == name {
			data, err := os.ReadFile(s.FilePath)
			if err != nil {
				return "", fmt.Errorf("load skill %q: %w", name, err)
			}
			return fmt.Sprintf("<skill_content name=%q base_dir=%q>\n%s\n</skill_content>", s.Name, s.BaseDir, data), nil
		}
	}

	return "", fmt.Errorf("skill %q not found", name)
}
