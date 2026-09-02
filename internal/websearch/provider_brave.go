package websearch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

var braveProvider = provider{
	name:      "brave",
	available: envSet("BRAVE_SEARCH_API_KEY"),
	search:    searchBrave,
}

func searchBrave(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	endpoint, _ := url.Parse("https://api.search.brave.com/res/v1/web/search")
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("count", strconv.Itoa(limit))
	endpoint.RawQuery = params.Encode()
	var response map[string]any
	if err := requestJSON(ctx, client, "brave", http.MethodGet, endpoint.String(), http.Header{"X-Subscription-Token": []string{get("BRAVE_SEARCH_API_KEY")}}, nil, &response); err != nil {
		return nil, err
	}
	web, ok := response["web"].(map[string]any)
	if !ok {
		return nil, errors.New("brave: response has no web result object")
	}
	return rows(web["results"])
}
