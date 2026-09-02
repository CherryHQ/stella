package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/httpclient"
)

const maxProviderResponseBytes = 1 * 1024 * 1024

type environment func(string) string

type sourceResult struct {
	Title   string
	URL     string
	Snippet string
	Score   float64
}

type searchProvider interface {
	Name() string
	Available(environment) bool
	Search(context.Context, *http.Client, environment, string, int) ([]sourceResult, error)
}

type provider struct {
	name      string
	available func(environment) bool
	search    func(context.Context, *http.Client, environment, string, int) ([]sourceResult, error)
}

func (p provider) Name() string                      { return p.name }
func (p provider) Available(getenv environment) bool { return p.available(getenv) }
func (p provider) Search(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
	return p.search(ctx, client, getenv, query, limit)
}

// providerOrder is the single native-env resolver order. It matches Hermes's
// credentialed search preference, but retries later configured providers after
// a provider error instead of pinning a whole Stella deployment to one outage.
func providerOrder() []searchProvider {
	return []searchProvider{
		provider{name: "firecrawl", available: func(get environment) bool {
			return strings.TrimSpace(get("FIRECRAWL_API_KEY")) != "" || strings.TrimSpace(get("FIRECRAWL_API_URL")) != ""
		}, search: searchFirecrawl},
		provider{name: "parallel", available: hasEnv("PARALLEL_API_KEY"), search: searchParallel},
		provider{name: "tavily", available: hasEnv("TAVILY_API_KEY"), search: searchTavily},
		provider{name: "exa", available: hasEnv("EXA_API_KEY"), search: searchExa},
		provider{name: "searxng", available: hasEnv("SEARXNG_URL"), search: searchSearXNG},
		provider{name: "brave", available: hasEnv("BRAVE_SEARCH_API_KEY"), search: searchBrave},
		provider{name: "keenable", available: hasEnv("KEENABLE_API_KEY"), search: searchKeenable},
	}
}

func hasEnv(name string) func(environment) bool {
	return func(get environment) bool { return strings.TrimSpace(get(name)) != "" }
}

func newProviderClient() *http.Client {
	client := httpclient.StdHTTPClient()
	client.Timeout = 30 * time.Second
	return client
}

func defaultEnvironment(name string) string { return os.Getenv(name) }

func requestJSON(ctx context.Context, client *http.Client, providerName, method, endpoint string, headers http.Header, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("%s: encode request", providerName)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("%s: create request", providerName)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed", providerName)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("%s: returned HTTP %d", providerName, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%s: read response", providerName)
	}
	if len(data) > maxProviderResponseBytes {
		return fmt.Errorf("%s: response exceeds 1 MB limit", providerName)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: returned invalid JSON", providerName)
	}
	return nil
}

func searchBrave(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
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
	if err := requestJSON(ctx, client, "brave", http.MethodGet, endpoint.String(), http.Header{"X-Subscription-Token": []string{getenv("BRAVE_SEARCH_API_KEY")}}, nil, &response); err != nil {
		return nil, err
	}
	out := make([]sourceResult, 0, len(response.Web.Results))
	for _, hit := range response.Web.Results {
		out = append(out, sourceResult{Title: hit.Title, URL: hit.URL, Snippet: hit.Description})
	}
	return out, nil
}

func searchTavily(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
	base := strings.TrimRight(strings.TrimSpace(getenv("TAVILY_BASE_URL")), "/")
	if base == "" {
		base = "https://api.tavily.com"
	}
	var response map[string]any
	err := requestJSON(ctx, client, "tavily", http.MethodPost, base+"/search", http.Header{"Authorization": []string{"Bearer " + getenv("TAVILY_API_KEY")}}, map[string]any{
		"query": query, "max_results": limit, "include_raw_content": false, "include_images": false,
	}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"]), nil
}

func searchExa(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
	var response map[string]any
	err := requestJSON(ctx, client, "exa", http.MethodPost, "https://api.exa.ai/search", http.Header{"x-api-key": []string{getenv("EXA_API_KEY")}}, map[string]any{
		"query": query, "numResults": limit, "contents": map[string]any{"highlights": map[string]any{}},
	}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"]), nil
}

func searchFirecrawl(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
	base := strings.TrimRight(strings.TrimSpace(getenv("FIRECRAWL_API_URL")), "/")
	if base == "" {
		base = "https://api.firecrawl.dev"
	}
	headers := http.Header{}
	if key := strings.TrimSpace(getenv("FIRECRAWL_API_KEY")); key != "" {
		headers.Set("Authorization", "Bearer "+key)
	}
	var response map[string]any
	if err := requestJSON(ctx, client, "firecrawl", http.MethodPost, base+"/v2/search", headers, map[string]any{"query": query, "limit": limit}, &response); err != nil {
		return nil, err
	}
	return firecrawlRows(response), nil
}

func searchParallel(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
	var response map[string]any
	mode := strings.TrimSpace(getenv("PARALLEL_SEARCH_MODE"))
	if mode == "" {
		mode = "agentic"
	}
	if mode != "fast" && mode != "one-shot" && mode != "agentic" {
		mode = "agentic"
	}
	err := requestJSON(ctx, client, "parallel", http.MethodPost, "https://api.parallel.ai/v1beta/search", http.Header{"Authorization": []string{"Bearer " + getenv("PARALLEL_API_KEY")}}, map[string]any{
		"search_queries": []string{query}, "objective": query, "mode": mode, "max_results": limit,
	}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"]), nil
}

func searchKeenable(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
	var response map[string]any
	err := requestJSON(ctx, client, "keenable", http.MethodPost, "https://api.keenable.ai/v1/search", http.Header{
		"Authorization":    []string{"Bearer " + getenv("KEENABLE_API_KEY")},
		"X-Keenable-Title": []string{"stella"},
	}, map[string]any{"query": query, "max_results": limit}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"]), nil
}

func searchSearXNG(ctx context.Context, client *http.Client, getenv environment, query string, limit int) ([]sourceResult, error) {
	base := strings.TrimRight(strings.TrimSpace(getenv("SEARXNG_URL")), "/")
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

func firecrawlRows(response map[string]any) []sourceResult {
	if values, ok := response["data"].([]any); ok {
		return rows(values)
	}
	if nested, ok := response["data"].(map[string]any); ok {
		if out := rows(nested["web"]); len(out) > 0 {
			return out
		}
		return rows(nested["results"])
	}
	if out := rows(response["web"]); len(out) > 0 {
		return out
	}
	return rows(response["results"])
}

func rows(value any) []sourceResult {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]sourceResult, 0, len(values))
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, sourceResult{
			Title:   stringValue(row, "title", "name"),
			URL:     stringValue(row, "url", "href", "link"),
			Snippet: stringValue(row, "description", "snippet", "content", "body", "highlights", "excerpts"),
			Score:   numberValue(row["score"]),
		})
	}
	return out
}

func stringValue(row map[string]any, names ...string) string {
	for _, name := range names {
		switch value := row[name].(type) {
		case string:
			if value != "" {
				return value
			}
		case []any:
			parts := make([]string, 0, len(value))
			for _, item := range value {
				if text, ok := item.(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

func numberValue(value any) float64 {
	switch value := value.(type) {
	case float64:
		return value
	case json.Number:
		parsed, _ := value.Float64()
		return parsed
	default:
		return 0
	}
}
