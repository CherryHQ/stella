package websearch

import (
	"context"
	"net/http"
)

type exaProvider struct{}

func (exaProvider) Name() string { return "exa" }

func (exaProvider) Available(get environment) bool { return hasEnv(get, "EXA_API_KEY") }

func (exaProvider) Validate(environment) error { return nil }

func (exaProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	var response map[string]any
	err := requestJSON(ctx, client, "exa", http.MethodPost, "https://api.exa.ai/search", http.Header{"x-api-key": []string{get("EXA_API_KEY")}}, map[string]any{
		"query": query, "numResults": limit, "contents": map[string]any{"highlights": map[string]any{}},
	}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"])
}
