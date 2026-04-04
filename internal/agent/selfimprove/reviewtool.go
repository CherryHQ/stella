package selfimprove

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vaayne/anna/internal/skills"
	"github.com/vaayne/anna/internal/toolspec"
)

var reviewSkillsInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create", "patch", "deprecate"],
      "description": "Action to perform on skills: 'create' makes a new draft skill, 'patch' updates an existing skill, 'deprecate' marks a skill as deprecated"
    },
    "name": {
      "type": "string",
      "description": "Skill name (lowercase-hyphenated, required for all actions)"
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
      "description": "Skill status (optional, for patch only)"
    }
  },
  "required": ["action", "name"]
}`), &m)
	return m
}()

// ReviewSkillsTool is a restricted skills tool that only allows create/patch/deprecate.
// It delegates to skills.Create, skills.Patch, and skills.Deprecate.
type ReviewSkillsTool struct {
	targetDir string // writable skills directory (per-user)
}

// NewReviewSkillsTool creates a ReviewSkillsTool for the given target directory.
func NewReviewSkillsTool(targetDir string) *ReviewSkillsTool {
	return &ReviewSkillsTool{targetDir: targetDir}
}

// Definition returns the tool definition for the LLM.
func (t *ReviewSkillsTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        "review_skills",
		Description: "Manage skills extracted from conversation review. Only supports create (new draft), patch (update existing), and deprecate.",
		InputSchema: reviewSkillsInputSchema,
	}
}

// Execute runs the review skills tool action.
func (t *ReviewSkillsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	name, _ := args["name"].(string)

	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	switch action {
	case "create":
		return t.create(name, args)
	case "patch":
		return t.patch(name, args)
	case "deprecate":
		return t.deprecate(name)
	default:
		return "", fmt.Errorf("unknown action %q, expected create/patch/deprecate", action)
	}
}

func (t *ReviewSkillsTool) create(name string, args map[string]any) (string, error) {
	description, _ := args["description"].(string)
	content, _ := args["content"].(string)

	if err := skills.Create(name, description, content, t.targetDir); err != nil {
		return "", err
	}
	return fmt.Sprintf("Skill %q created as draft.", name), nil
}

func (t *ReviewSkillsTool) patch(name string, args map[string]any) (string, error) {
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

	if err := skills.Patch(name, updates, t.targetDir); err != nil {
		return "", err
	}
	return fmt.Sprintf("Skill %q updated.", name), nil
}

func (t *ReviewSkillsTool) deprecate(name string) (string, error) {
	if err := skills.Deprecate(name, t.targetDir); err != nil {
		return "", err
	}
	return fmt.Sprintf("Skill %q deprecated.", name), nil
}
