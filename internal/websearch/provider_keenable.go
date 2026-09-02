package websearch

import (
	"context"
	"net/http"
)

var keenableProvider = provider{
	name:      "keenable",
	available: envSet("KEENABLE_API_KEY"),
	search:    searchKeenable,
}

func searchKeenable(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
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
