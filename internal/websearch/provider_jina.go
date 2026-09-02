package websearch

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

var jinaProvider = provider{
	name:      "jina",
	available: envSet("JINA_API_KEY"),
	search:    searchJina,
}

func searchJina(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	endpoint := "https://s.jina.ai/" + url.PathEscape(query) + "?count=" + strconv.Itoa(limit)
	var response map[string]any
	if err := requestJSON(ctx, client, "jina", http.MethodGet, endpoint, http.Header{
		"Authorization":   []string{"Bearer " + get("JINA_API_KEY")},
		"User-Agent":      []string{"Stella/1.0"},
		"X-Respond-With":  []string{"no-content"},
		"X-Retain-Images": []string{"none"},
	}, nil, &response); err != nil {
		return nil, err
	}
	return rows(response["data"])
}
