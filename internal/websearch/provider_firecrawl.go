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

// firecrawlRows reads the v2 /search shape: {"data":{"web":[...]}}.
func firecrawlRows(response map[string]any) ([]sourceResult, error) {
	data, ok := response["data"].(map[string]any)
	if !ok {
		return nil, errors.New("firecrawl: response has no data object")
	}
	return rows(data["web"])
}
