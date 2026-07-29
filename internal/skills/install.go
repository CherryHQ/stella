package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	mcpskills "github.com/vaayne/mcphub/pkg/skills"

	"github.com/CherryHQ/stella/internal/authz"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// skillSearchResult is a normalized result combining both search providers.
type skillSearchResult struct {
	Provider    string `json:"provider"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
}

func (t *Tool) search(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required for search action")
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	results, errs := searchRemoteCatalogs(ctx, query, limit, clawhubSearch, mcpskills.Search)
	if len(results) == 0 {
		if len(errs) > 0 {
			return "", fmt.Errorf("search failed: %s", errs[0])
		}
		return "No skills found.", nil
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	msg := fmt.Sprintf("Found %d skills:\n%s\n\nInstall with: skills tool action=install source=<source from results above>\nOptional: add scope=\"agent\" to install into agent scope.", len(results), out)
	if len(errs) > 0 {
		msg += fmt.Sprintf("\n\nNote: some providers failed: %v", errs)
	}
	return msg, nil
}

// searchRemoteCatalogs normalizes the two advertised remote catalogs behind one
// deterministic helper. Tests inject protocol results here and can carry the
// returned source through the real fetch/install/load/upgrade lifecycle.
func searchRemoteCatalogs(
	ctx context.Context,
	query string,
	limit int,
	searchClawhub func(context.Context, string, int) ([]clawhubSearchResult, error),
	searchSkillsSh func(context.Context, string, int) ([]mcpskills.SearchResult, error),
) ([]skillSearchResult, []string) {
	var (
		mu      sync.Mutex
		results []skillSearchResult
		errs    []string
		wg      sync.WaitGroup
	)

	// Search clawhub.ai concurrently.
	wg.Go(func() {
		hits, err := searchClawhub(ctx, query, limit)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Sprintf("clawhub: %v", err))
			return
		}
		for _, h := range hits {
			r := skillSearchResult{
				Provider: "clawhub",
				Name:     h.Slug,
				Source:   "clawhub:" + h.Slug,
			}
			if h.Summary != "" {
				r.Description = h.Summary
			} else if h.DisplayName != "" {
				r.Description = h.DisplayName
			}
			results = append(results, r)
		}
	})

	// Search skills.sh concurrently.
	wg.Go(func() {
		hits, err := searchSkillsSh(ctx, query, limit)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Sprintf("skills.sh: %v", err))
			return
		}
		for _, h := range hits {
			source := h.Source
			if h.SkillID != "" {
				source = h.Source + "@" + h.SkillID
			}
			results = append(results, skillSearchResult{
				Provider:    "skills.sh",
				Name:        h.Name,
				Description: h.Source,
				Source:      source,
			})
		}
	})

	wg.Wait()
	return results, errs
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
	scope, err := t.targetScope(ctx, rawScope)
	if err != nil {
		return "", err
	}

	if t.store == nil {
		return "", fmt.Errorf("skills store unavailable")
	}

	skillName, files, version, cleanup, err := FetchSkillFiles(ctx, source)
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
	if version != "" {
		metaJSON = fmt.Sprintf(`{"created-at":%q,"version":%q}`, createdAt, version)
	}

	vc := pkgplugins.SkillViewContext{
		UserID:  authz.UserIDFromContext(ctx),
		AgentID: authz.AgentIDFromContext(ctx),
	}

	sk := pkgplugins.Skill{
		Scope:                  scope,
		Name:                   name,
		Description:            fm.Description,
		Status:                 SkillStatusActive,
		DisableModelInvocation: fm.DisableModelInvocation,
		Metadata:               json.RawMessage(metaJSON),
	}
	switch scope {
	case "user":
		sk.UserID = vc.UserID
	case "user_agent":
		sk.UserID = vc.UserID
		sk.AgentID = vc.AgentID
	case "system_agent":
		sk.AgentID = vc.AgentID
	}

	// Authorize the create before touching the store. Even though the model-facing
	// tool never exposes install today (actionsOnly), installing a DB skill is a
	// durable write and must fail closed without an injected write authorizer,
	// matching create/patch.
	if err := t.authorizeCreate(ctx, scope, sk.AgentID); err != nil {
		return "", err
	}

	if _, err := t.store.Create(ctx, sk, files); err != nil {
		return "", fmt.Errorf("store skill %q: %w", name, err)
	}

	return fmt.Sprintf("Skill %q installed (scope=%s).", name, scope), nil
}
