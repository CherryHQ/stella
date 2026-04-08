package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	pkgskills "github.com/vaayne/anna/pkg/skills"
	mcpskills "github.com/vaayne/mcphub/pkg/skills"
)

func (t *Tool) search(ctx context.Context, args map[string]any) (string, error) {
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

func (t *Tool) install(ctx context.Context, args map[string]any) (string, error) {
	source, _ := args["source"].(string)
	if source == "" {
		return "", fmt.Errorf("source is required for install action (e.g. owner/repo@skill-name)")
	}

	targetDir := t.skillsDir()
	skillName, err := pkgskills.Install(ctx, source, targetDir)
	if err != nil {
		return "", err
	}

	installed := filepath.Join(targetDir, skillName)
	return fmt.Sprintf("Skill %q installed to %s.", skillName, installed), nil
}

func (t *Tool) remove(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for remove action")
	}

	skillDir := filepath.Join(t.skillsDir(), name)
	if err := pkgskills.Remove(name, skillDir); err != nil {
		return "", fmt.Errorf("%w (only skills in %s can be removed)", err, t.skillsDir())
	}

	return fmt.Sprintf("Skill %q removed from %s.", name, skillDir), nil
}
