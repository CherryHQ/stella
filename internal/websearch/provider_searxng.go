package websearch

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type searxngProvider struct{}

func (searxngProvider) Name() string { return "searxng" }

func (searxngProvider) Available(get environment) bool { return hasEnv(get, "SEARXNG_URL") }

func (searxngProvider) Validate(get environment) error {
	return validHTTPURL(get("SEARXNG_URL"), "SEARXNG_URL")
}

func (searxngProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	base := strings.TrimRight(strings.TrimSpace(get("SEARXNG_URL")), "/")
	endpoint, _ := url.Parse(base + "/search")
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("pageno", "1")
	endpoint.RawQuery = params.Encode()
	var response map[string]any
	if err := requestJSON(ctx, client, "searxng", http.MethodGet, endpoint.String(), nil, nil, &response); err != nil {
		return nil, err
	}
	out, err := rows(response["results"])
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}
