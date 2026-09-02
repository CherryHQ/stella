package websearch

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

var firecrawlProvider = provider{
	name: "firecrawl",
	available: func(get environment) bool {
		return hasEnv(get, "FIRECRAWL_API_KEY") || hasEnv(get, "FIRECRAWL_API_URL")
	},
	validate: optionalURL("FIRECRAWL_API_URL"),
	search:   searchFirecrawl,
}

func searchFirecrawl(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
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
	return firecrawlRows(response)
}

func firecrawlRows(response map[string]any) ([]sourceResult, error) {
	if data, ok := response["data"]; ok {
		switch data := data.(type) {
		case []any:
			return rows(data)
		case map[string]any:
			if values, ok := data["web"]; ok {
				return rows(values)
			}
			if values, ok := data["results"]; ok {
				return rows(values)
			}
		}
	}
	if values, ok := response["web"]; ok {
		return rows(values)
	}
	if values, ok := response["results"]; ok {
		return rows(values)
	}
	return nil, errors.New("firecrawl: response has no result list")
}
