package websearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

var parallelProvider = provider{
	name:      "parallel",
	available: envSet("PARALLEL_API_KEY"),
	validate: func(get environment) error {
		mode := strings.TrimSpace(get("PARALLEL_SEARCH_MODE"))
		if mode == "" || mode == "fast" || mode == "one-shot" || mode == "agentic" {
			return nil
		}
		return fmt.Errorf("PARALLEL_SEARCH_MODE must be agentic, fast, or one-shot")
	},
	search: searchParallel,
}

func searchParallel(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	mode := strings.TrimSpace(get("PARALLEL_SEARCH_MODE"))
	if mode == "" {
		mode = "agentic"
	}
	var response map[string]any
	err := requestJSON(ctx, client, "parallel", http.MethodPost, "https://api.parallel.ai/v1beta/search", http.Header{"X-API-Key": []string{get("PARALLEL_API_KEY")}}, map[string]any{
		"search_queries": []string{query}, "objective": query, "mode": mode, "max_results": limit,
	}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"])
}
