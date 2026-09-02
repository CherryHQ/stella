package websearch

import (
	"context"
	"net/http"
)

type keenableProvider struct{}

func (keenableProvider) Name() string { return "keenable" }

func (keenableProvider) Available(get environment) bool { return hasEnv(get, "KEENABLE_API_KEY") }

func (keenableProvider) Validate(environment) error { return nil }

func (keenableProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	var response map[string]any
	err := requestJSON(ctx, client, "keenable", http.MethodPost, "https://api.keenable.ai/v1/search", http.Header{
		"Authorization":    []string{"Bearer " + get("KEENABLE_API_KEY")},
		"X-Keenable-Title": []string{"stella"},
	}, map[string]any{"query": query, "max_results": limit}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"])
}
