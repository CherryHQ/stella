package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vaayne/anna/internal/toolspec"
	mcpskills "github.com/vaayne/mcphub/pkg/skills"
)

var skillsInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["search", "install", "list", "remove"],
      "description": "Action to perform: 'search' finds skills from the ecosystem, 'install' adds a skill to the project, 'list' shows installed skills, 'remove' deletes an installed skill"
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
      "description": "Name of the installed skill (required for remove)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// SkillsTool exposes skill management as an agent tool.
type SkillsTool struct {
	annaHome  string
	workspace string
	cwd       string
}

// NewTool creates a SkillsTool for the given anna home, workspace and working directory.
func NewTool(annaHome, workspace, cwd string) *SkillsTool {
	return &SkillsTool{annaHome: annaHome, workspace: workspace, cwd: cwd}
}

// Definition returns the tool definition for the LLM.
func (t *SkillsTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        "skills",
		Description: "Manage agent skills. Use 'search' to find skills from the ecosystem, 'install' to add a skill (e.g. owner/repo@skill-name), 'list' to see installed skills, 'remove' to delete one.",
		InputSchema: skillsInputSchema,
	}
}

// Execute runs the skills tool action.
func (t *SkillsTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "search":
		return t.search(ctx, args)
	case "install":
		return t.install(ctx, args)
	case "list":
		return t.list()
	case "remove":
		return t.remove(args)
	default:
		return "", fmt.Errorf("unknown action %q, expected search/install/list/remove", action)
	}
}

func (t *SkillsTool) search(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required for search action")
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	results, err := mcpskills.Search(ctx, query, limit)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "No skills found.", nil
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	return fmt.Sprintf("Found %d skills:\n%s\n\nInstall with: skills tool action=install source=\"owner/repo@skill-name\"", len(results), out), nil
}
