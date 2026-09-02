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

// searchProvider hides provider-specific credentials, HTTP shape, and response
// decoding behind one normalized search operation. The resolver owns ordering
// and fallback; provider implementations own only their native API contract.
type searchProvider interface {
	Name() string
	Available(environment) bool
	Validate(environment) error
	Search(context.Context, *http.Client, environment, string, int) ([]sourceResult, error)
}

// providerOrder is the single native-env resolver order. It matches Hermes's
// credentialed search preference, but retries later configured providers after
// a provider error instead of pinning a whole Stella deployment to one outage.
func providerOrder() []searchProvider {
	return []searchProvider{
		firecrawlProvider{},
		parallelProvider{},
		tavilyProvider{},
		exaProvider{},
		searxngProvider{},
		braveProvider{},
		keenableProvider{},
	}
}

func hasEnv(get environment, name string) bool { return strings.TrimSpace(get(name)) != "" }

func newProviderClient() *http.Client {
	client := httpclient.StdHTTPClient()
	client.Timeout = 30 * time.Second
	// Native provider credentials must never follow a redirect to a host the
	// administrator did not configure. Provider APIs do not require redirects.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("provider redirects are not allowed")
	}
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
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
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

// rows rejects a syntactically valid but structurally invalid provider response.
// A silent empty result would otherwise stop the resolver before its fallback.
func rows(value any) ([]sourceResult, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("search results field is missing or invalid")
	}
	out := make([]sourceResult, 0, len(values))
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("search result row is invalid")
		}
		score, _ := row["score"].(float64)
		out = append(out, sourceResult{
			Title:   stringValue(row, "title", "name"),
			URL:     stringValue(row, "url", "href", "link"),
			Snippet: stringValue(row, "description", "snippet", "content", "body", "highlights", "excerpts"),
			Score:   score,
		})
	}
	return out, nil
}

func validHTTPURL(value, name string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("%s must be an http or https URL without userinfo", name)
	}
	return nil
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
