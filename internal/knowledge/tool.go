package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ToolName is underscore-separated because provider function names do not
// consistently accept dots. Product documentation may refer to the logical
// operation as knowledge.search.
const ToolName = "knowledge_search"

type knowledgeSearcher interface {
	Search(ctx context.Context, userID, agentID, query string, limit int) ([]SearchResult, error)
}

// Tool exposes the current session's four-scope Knowledge Base union to an
// Agent without accepting any caller-controlled identity or scope filters.
type Tool struct {
	searcher knowledgeSearcher
}

// NewTool builds the single read-only Knowledge Base Agent tool.
func NewTool(service *Service) *Tool {
	return &Tool{searcher: service}
}

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name: ToolName,
		Description: "Search uploaded Knowledge Base files visible to the current user and agent. " +
			"Returns complete evidence chunks with file names and citation locators; " +
			"it does not search conversation memory.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"maxLength":   MaxSearchQueryRunes,
					"description": "One focused keyword query derived from the current request and relevant conversation context.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     10,
					"default":     5,
					"description": "Maximum number of chunks to return.",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("%s query must be a string", ToolName)
	}
	limit, err := parseSearchLimit(args["limit"])
	if err != nil {
		return "", err
	}

	userID := authz.UserIDFromContext(ctx)
	agentID := authz.AgentIDFromContext(ctx)
	if userID == "" || agentID == "" {
		// The normal registry gate prevents this path. Returning an empty result
		// keeps direct or stale invocations fail-closed without leaking whether
		// any system-scoped documents exist.
		return marshalSearchResponse([]SearchResult{})
	}
	if t == nil || t.searcher == nil {
		return "", ErrServiceUnavailable
	}

	results, err := t.searcher.Search(ctx, userID, agentID, query, limit)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolName, err)
	}
	return marshalSearchResponse(results)
}

func parseSearchLimit(value any) (int, error) {
	if value == nil {
		return 5, nil
	}
	var limit int
	switch typed := value.(type) {
	case int:
		limit = typed
	case int32:
		limit = int(typed)
	case int64:
		limit = int(typed)
	case float64:
		if typed != math.Trunc(typed) {
			return 0, fmt.Errorf("%s limit must be an integer", ToolName)
		}
		limit = int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s limit must be an integer", ToolName)
		}
		limit = int(parsed)
	default:
		return 0, fmt.Errorf("%s limit must be an integer", ToolName)
	}
	if limit < 1 || limit > 10 {
		return 0, fmt.Errorf("%s limit must be between 1 and 10", ToolName)
	}
	return limit, nil
}

func marshalSearchResponse(results []SearchResult) (string, error) {
	if results == nil {
		results = []SearchResult{}
	}
	return tools.MarshalResult(struct {
		Results []SearchResult `json:"results"`
	}{Results: results})
}
