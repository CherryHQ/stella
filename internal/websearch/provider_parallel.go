package websearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type parallelProvider struct{}

func (parallelProvider) Name() string { return "parallel" }

func (parallelProvider) Available(get environment) bool { return hasEnv(get, "PARALLEL_API_KEY") }

func (parallelProvider) Validate(get environment) error {
	mode := strings.TrimSpace(get("PARALLEL_SEARCH_MODE"))
	if mode == "" || mode == "fast" || mode == "one-shot" || mode == "agentic" {
		return nil
	}
	return fmt.Errorf("PARALLEL_SEARCH_MODE must be agentic, fast, or one-shot")
}

func (parallelProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
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
