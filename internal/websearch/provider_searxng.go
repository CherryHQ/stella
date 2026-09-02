package websearch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type searxngProvider struct{}

func (searxngProvider) Name() string { return "searxng" }

func (searxngProvider) Available(get environment) bool { return hasEnv(get, "SEARXNG_URL") }

func (searxngProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	base := strings.TrimRight(strings.TrimSpace(get("SEARXNG_URL")), "/")
	endpoint, err := url.Parse(base + "/search")
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("searxng: SEARXNG_URL must be an http or https URL")
	}
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("pageno", "1")
	endpoint.RawQuery = params.Encode()
	var response map[string]any
	if err := requestJSON(ctx, client, "searxng", http.MethodGet, endpoint.String(), nil, nil, &response); err != nil {
		return nil, err
	}
	out := rows(response["results"])
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}
