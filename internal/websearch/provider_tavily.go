package websearch

import (
	"context"
	"net/http"
	"strings"
)

type tavilyProvider struct{}

func (tavilyProvider) Name() string { return "tavily" }

func (tavilyProvider) Available(get environment) bool { return hasEnv(get, "TAVILY_API_KEY") }

func (tavilyProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	base := strings.TrimRight(strings.TrimSpace(get("TAVILY_BASE_URL")), "/")
	if base == "" {
		base = "https://api.tavily.com"
	}
	var response map[string]any
	err := requestJSON(ctx, client, "tavily", http.MethodPost, base+"/search", http.Header{"Authorization": []string{"Bearer " + get("TAVILY_API_KEY")}}, map[string]any{
		"query": query, "max_results": limit, "include_raw_content": false, "include_images": false,
	}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"]), nil
}
