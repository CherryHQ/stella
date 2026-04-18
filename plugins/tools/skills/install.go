package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
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
	return fmt.Sprintf("Found %d skills:\n%s\n\nInstall with: skills tool action=install source=\"owner/repo@skill-name\"\nOptional: add scope=\"project\" to install into ProjectRoot/.agents/skills.", len(results), out), nil
}

func (t *Tool) install(ctx context.Context, args map[string]any) (string, error) {
	source, _ := args["source"].(string)
	if source == "" {
		return "", fmt.Errorf("source is required for install action (e.g. owner/repo@skill-name)")
	}

	// Parse + validate scope before touching the store.
	rawScope, err := scopeArg(args)
	if err != nil {
		return "", err
	}
	scope, _, err := t.targetSkillsDir(ctx, rawScope)
	if err != nil {
		return "", err
	}

	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}

	skillName, files, cleanup, err := FetchSkillFiles(ctx, source)
	if err != nil {
		return "", err
	}
	defer cleanup()

	mainContent, ok := files[pkgplugins.SkillMainFile]
	if !ok {
		return "", fmt.Errorf("fetched skill %q is missing SKILL.md", skillName)
	}

	fm, err := parseFrontmatter(mainContent)
	if err != nil {
		return "", fmt.Errorf("parse SKILL.md for %q: %w", skillName, err)
	}

	// Use name from frontmatter if available, fall back to dir name.
	name := fm.Name
	if name == "" {
		name = skillName
	}

	createdAt := fm.CreatedAt
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}

	metaJSON := fmt.Sprintf(`{"created-at":%q}`, createdAt)

	vc := pkgplugins.SkillViewContext{
		UserID:  memory.UserIDFromContext(ctx),
		AgentID: memory.AgentIDFromContext(ctx),
		Project: projectRootFromContext(ctx, t.projectRoot),
	}

	sk := pkgplugins.Skill{
		Scope:                  scope,
		Name:                   name,
		Description:            fm.Description,
		Status:                 NormalizeSkillStatus(fm.Status),
		DisableModelInvocation: fm.DisableModelInvocation,
		Metadata:               json.RawMessage(metaJSON),
	}
	switch scope {
	case "user":
		sk.UserID = vc.UserID
	case "project":
		sk.Project = vc.Project
	}

	if _, err := t.store.Create(ctx, sk, files); err != nil {
		return "", fmt.Errorf("store skill %q: %w", name, err)
	}

	return fmt.Sprintf("Skill %q installed (scope=%s).", name, scope), nil
}
