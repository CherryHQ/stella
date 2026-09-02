package websearch

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type braveProvider struct{}

func (braveProvider) Name() string { return "brave" }

func (braveProvider) Available(get environment) bool { return hasEnv(get, "BRAVE_SEARCH_API_KEY") }

func (braveProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	endpoint, _ := url.Parse("https://api.search.brave.com/res/v1/web/search")
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("count", strconv.Itoa(limit))
	endpoint.RawQuery = params.Encode()
	var response struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := requestJSON(ctx, client, "brave", http.MethodGet, endpoint.String(), http.Header{"X-Subscription-Token": []string{get("BRAVE_SEARCH_API_KEY")}}, nil, &response); err != nil {
		return nil, err
	}
	out := make([]sourceResult, 0, len(response.Web.Results))
	for _, hit := range response.Web.Results {
		out = append(out, sourceResult{Title: hit.Title, URL: hit.URL, Snippet: hit.Description})
	}
	return out, nil
}
