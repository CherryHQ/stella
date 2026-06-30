package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/CherryHQ/stella/internal/searchrank"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type installedSkillSearchResult struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Status        string   `json:"status"`
	Scope         string   `json:"scope"`
	MatchedFields []string `json:"matched_fields"`
	Snippet       string   `json:"snippet"`
	Score         float64  `json:"score"`
}

func (t *Tool) searchInstalled(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required for search_installed action")
	}
	limit := installedSkillSearchLimit(args)

	projectRoot := projectRootFromContext(ctx, t.projectRoot)
	merged, err := t.svc.ListMerged(ctx, t.viewContext(ctx), projectRoot)
	if err != nil {
		return "", fmt.Errorf("search installed skills: %w", err)
	}

	skills := t.visibleSearchableSkills(merged)
	if len(skills) == 0 {
		return "No installed skills found.", nil
	}

	docs := make([]searchrank.Document, 0, len(skills))
	byName := make(map[string]pkgplugins.Skill, len(skills))
	for _, skill := range skills {
		byName[skill.Name] = skill
		docs = append(docs, searchrank.Document{
			ID: skill.Name,
			Fields: []searchrank.Field{
				{Name: "description", Text: skill.Description, Weight: 3},
				{Name: "name", Text: skill.Name, Weight: 1.5},
			},
		})
	}

	ranked := searchrank.Rank(query, docs, len(docs))
	if len(ranked) == 0 {
		return "No installed skills found.", nil
	}
	boostSkillNameMatches(query, ranked)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	results := make([]installedSkillSearchResult, 0, len(ranked))
	for _, hit := range ranked {
		skill := byName[hit.ID]
		results = append(results, installedSkillSearchResult{
			Name:          skill.Name,
			Description:   skill.Description,
			Status:        skill.Status,
			Scope:         skill.Scope,
			MatchedFields: hit.MatchedFields,
			Snippet:       hit.Snippet,
			Score:         hit.Score,
		})
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	return string(out), nil
}

func (t *Tool) visibleSearchableSkills(merged []ResolvedSkill) []pkgplugins.Skill {
	all := make([]pkgplugins.Skill, 0, len(merged))
	for _, rs := range merged {
		all = append(all, rs.Skill)
	}
	all = filterVisibleSkills(all, pkgplugins.SystemPromptContext{
		RegisteredPluginIDs: t.registeredPluginIDs,
		EnabledPluginIDs:    t.enabledPluginIDs,
	})

	out := make([]pkgplugins.Skill, 0, len(all))
	for _, skill := range all {
		if skill.Status == SkillStatusDeprecated || skill.DisableModelInvocation {
			continue
		}
		out = append(out, skill)
	}
	return out
}

func installedSkillSearchLimit(args map[string]any) int {
	const (
		defaultLimit = 10
		maxLimit     = 100
	)
	limit := defaultLimit
	switch v := args["limit"].(type) {
	case float64:
		if v > 0 {
			limit = int(v)
		}
	case int:
		if v > 0 {
			limit = v
		}
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

func boostSkillNameMatches(query string, hits []searchrank.Result) {
	q := strings.ToLower(strings.TrimSpace(query))
	for i := range hits {
		name := strings.ToLower(hits[i].ID)
		switch {
		case name == q:
			hits[i].Score += 3
		case strings.Contains(name, q) || strings.Contains(q, name):
			hits[i].Score += 1
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].ID < hits[j].ID
		}
		return hits[i].Score > hits[j].Score
	})
}
