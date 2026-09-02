package websearch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

type jinaProvider struct{}

func (jinaProvider) Name() string { return "jina" }

func (jinaProvider) Available(get environment) bool { return hasEnv(get, "JINA_API_KEY") }

func (jinaProvider) Validate(environment) error { return nil }

func (jinaProvider) Search(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	endpoint := "https://s.jina.ai/" + url.PathEscape(query) + "?count=" + strconv.Itoa(limit)
	var response any
	if err := requestJSON(ctx, client, "jina", http.MethodGet, endpoint, http.Header{
		"Authorization":   []string{"Bearer " + get("JINA_API_KEY")},
		"User-Agent":      []string{"Stella/1.0"},
		"X-Respond-With":  []string{"no-content"},
		"X-Retain-Images": []string{"none"},
	}, nil, &response); err != nil {
		return nil, err
	}
	if values, ok := response.([]any); ok {
		return rows(values)
	}
	if envelope, ok := response.(map[string]any); ok {
		return rows(envelope["data"])
	}
	return nil, errors.New("jina: response has no result list")
}
