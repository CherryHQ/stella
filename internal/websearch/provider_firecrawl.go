package websearch

import (
	"context"
	"net/http"
	"strings"
)

type firecrawlProvider struct{}

func (firecrawlProvider) Name() string { return "firecrawl" }

func (firecrawlProvider) Available(get environment) bool {
	return hasEnv(get, "FIRECRAWL_API_KEY") || hasEnv(get, "FIRECRAWL_API_URL")
}

func (firecrawlProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	base := strings.TrimRight(strings.TrimSpace(get("FIRECRAWL_API_URL")), "/")
	if base == "" {
		base = "https://api.firecrawl.dev"
	}
	headers := http.Header{}
	if key := strings.TrimSpace(get("FIRECRAWL_API_KEY")); key != "" {
		headers.Set("Authorization", "Bearer "+key)
	}
	var response map[string]any
	if err := requestJSON(ctx, client, "firecrawl", http.MethodPost, base+"/v2/search", headers, map[string]any{"query": query, "limit": limit}, &response); err != nil {
		return nil, err
	}
	return firecrawlRows(response), nil
}

func firecrawlRows(response map[string]any) []sourceResult {
	if values, ok := response["data"].([]any); ok {
		return rows(values)
	}
	if nested, ok := response["data"].(map[string]any); ok {
		if out := rows(nested["web"]); len(out) > 0 {
			return out
		}
		return rows(nested["results"])
	}
	if out := rows(response["web"]); len(out) > 0 {
		return out
	}
	return rows(response["results"])
}
