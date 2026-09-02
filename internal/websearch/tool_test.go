package websearch

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func testTool(service *Service) *Tool { return NewTool(service, ActionTools()[0]) }

func toolContext(t *testing.T) context.Context {
	t.Helper()
	return authz.WithAgentID(authz.WithUserID(t.Context(), "user-1"), "agent-1")
}

func getenv(values map[string]string) environment {
	return func(name string) string { return values[name] }
}

func TestToolSearch(t *testing.T) {
	var request *http.Request
	service := newService(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		request = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"web":{"results":[
					{"title":"First", "url":"https://example.com/first", "description":"Useful evidence"},
					{"title":"Second", "url":"", "description":"Dropped without a URL"}
				]}
			}`)),
		}, nil
	})}, getenv(map[string]string{"BRAVE_SEARCH_API_KEY": "brave-secret"}), providerOrder())

	output, err := testTool(service).Execute(toolContext(t), map[string]any{"query": "stella research"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if request == nil {
		t.Fatal("search did not make a provider request")
	}
	if got := request.URL.Query().Get("q"); got != "stella research" {
		t.Errorf("query = %q, want stella research", got)
	}
	if got := request.URL.Query().Get("count"); got != "5" {
		t.Errorf("count = %q, want default 5", got)
	}
	if got := request.Header.Get("X-Subscription-Token"); got != "brave-secret" {
		t.Errorf("credential header = %q", got)
	}
	if strings.Contains(output, "brave-secret") {
		t.Fatalf("tool result leaked the provider credential: %q", output)
	}
	if !strings.Contains(output, `"untrusted":true`) || !strings.Contains(output, "https://example.com/first") {
		t.Fatalf("output = %s, want untrusted normalized result", output)
	}
	if !strings.Contains(output, `"truncated":true`) {
		t.Fatalf("output = %s, want dropped result to be reported", output)
	}
}

func TestProviderOrderEndsWithAnonymousExaFallback(t *testing.T) {
	providers := providerOrder()
	got := make([]string, 0, len(providers))
	for _, provider := range providers {
		got = append(got, provider.name)
	}
	want := []string{"firecrawl", "parallel", "tavily", "exa", "jina", "searxng", "brave", "keenable", "exa"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("provider order = %v, want %v", got, want)
	}
	last := providers[len(providers)-1]
	if !last.available(getenv(nil)) || last.available(getenv(map[string]string{"EXA_API_KEY": "key"})) {
		t.Fatalf("last provider = %q, want anonymous Exa MCP that yields to EXA_API_KEY", last.name)
	}
}

func TestNativeProvidersNormalizeResults(t *testing.T) {
	tests := []struct {
		name string
		want string
		env  map[string]string
		body string
	}{
		{"firecrawl", "Firecrawl", map[string]string{"FIRECRAWL_API_KEY": "key"}, `{"success":true,"data":{"web":[{"title":"Firecrawl","url":"https://example.com/","description":"snippet"}]}}`},
		{"parallel", "Parallel", map[string]string{"PARALLEL_API_KEY": "key"}, `{"results":[{"title":"Parallel","url":"https://example.com/","excerpts":["snippet"]}]}`},
		{"tavily", "Tavily", map[string]string{"TAVILY_API_KEY": "key"}, `{"results":[{"title":"Tavily","url":"https://example.com/","content":"snippet"}]}`},
		{"exa", "Exa", map[string]string{"EXA_API_KEY": "key"}, `{"results":[{"title":"Exa","url":"https://example.com/","highlights":["snippet"]}]}`},
		{"jina", "Jina", map[string]string{"JINA_API_KEY": "key"}, `{"data":[{"title":"Jina","url":"https://example.com/","description":"snippet"}]}`},
		{"searxng", "SearXNG", map[string]string{"SEARXNG_URL": "http://searx.test"}, `{"results":[{"title":"SearXNG","url":"https://example.com/","content":"snippet","score":1}]}`},
		{"brave", "Brave", map[string]string{"BRAVE_SEARCH_API_KEY": "key"}, `{"web":{"results":[{"title":"Brave","url":"https://example.com/","description":"snippet"}]}}`},
		{"keenable", "Keenable", map[string]string{"KEENABLE_API_KEY": "key"}, `{"results":[{"title":"Keenable","url":"https://example.com/","snippet":"snippet"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var selected *provider
			for _, candidate := range providerOrder() {
				if candidate.name == test.name {
					selected = &candidate
					break
				}
			}
			if selected == nil || !selected.available(getenv(test.env)) {
				t.Fatalf("provider %q is unavailable with its native environment", test.name)
			}
			results, err := selected.search(t.Context(), &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}, getenv(test.env), "stella", 1)
			if err != nil || len(results) != 1 || results[0].Title != test.want {
				t.Fatalf("Search() = %#v, %v", results, err)
			}
		})
	}
}

func TestJinaUsesNativeAPIKeyHeader(t *testing.T) {
	var request *http.Request
	_, err := jinaProvider.search(t.Context(), &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		request = req.Clone(req.Context())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
	})}, getenv(map[string]string{"JINA_API_KEY": "jina-secret"}), "stella research", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer jina-secret" {
		t.Errorf("Authorization = %q, want Jina bearer token", got)
	}
	if request.URL.Host != "s.jina.ai" || request.URL.Query().Get("count") != "10" {
		t.Errorf("request URL = %s, want Jina search with count=10", request.URL)
	}
}

func TestParallelUsesNativeAPIKeyHeader(t *testing.T) {
	var request *http.Request
	_, err := parallelProvider.search(t.Context(), &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		request = req.Clone(req.Context())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"results":[]}`))}, nil
	})}, getenv(map[string]string{"PARALLEL_API_KEY": "parallel-secret"}), "stella", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("X-API-Key"); got != "parallel-secret" {
		t.Errorf("X-API-Key = %q, want native API key", got)
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want no bearer header", got)
	}
}

func TestSearchFallsBackToNextConfiguredProvider(t *testing.T) {
	service := newService(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.firecrawl.dev" {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"unavailable"}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"web":{"results":[{"title":"Brave fallback","url":"https://example.com/","description":"ok"}]}}`))}, nil
	})}, getenv(map[string]string{
		"FIRECRAWL_API_KEY":    "firecrawl-key",
		"BRAVE_SEARCH_API_KEY": "brave-key",
	}), providerOrder())

	result, err := service.search(t.Context(), "stella", 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "brave" || len(result.Results) != 1 || result.Results[0].Title != "Brave fallback" {
		t.Fatalf("fallback result = %#v", result)
	}
}

type searchFiles struct{ files map[string][]byte }

func (f *searchFiles) ReadFile(name string) ([]byte, error)             { return f.files[name], nil }
func (*searchFiles) ReadDir(string) ([]sandbox.DirEntry, error)         { return nil, nil }
func (*searchFiles) Stat(string) (sandbox.FileInfo, error)              { return sandbox.FileInfo{}, nil }
func (*searchFiles) WriteFile(string, []byte, fs.FileMode) error        { return nil }
func (*searchFiles) ProjectFiles(string, []sandbox.ProjectedFile) error { return nil }
func (f *searchFiles) ProjectTempFiles(name string, files []sandbox.ProjectedFile) (string, error) {
	root := path.Join("/tmp", name)
	for _, file := range files {
		f.files[path.Join(root, file.Path)] = file.Content
	}
	return root, nil
}

func TestSearchFallsBackFromMalformedProviderResponse(t *testing.T) {
	service := newService(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.firecrawl.dev" {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"web":{"results":[{"title":"Brave fallback","url":"https://example.com/","description":"ok"}]}}`))}, nil
	})}, getenv(map[string]string{
		"FIRECRAWL_API_KEY":    "firecrawl-key",
		"BRAVE_SEARCH_API_KEY": "brave-key",
	}), providerOrder())

	result, err := service.search(t.Context(), "stella", 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "brave" {
		t.Fatalf("provider = %q, want fallback brave", result.Provider)
	}
}

func TestSearchStopsOnCanceledContext(t *testing.T) {
	calls := 0
	service := newService(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("request must not run")
	})}, getenv(map[string]string{
		"FIRECRAWL_API_KEY":    "firecrawl-key",
		"BRAVE_SEARCH_API_KEY": "brave-key",
	}), providerOrder())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := service.search(ctx, "stella", 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("search error = %v, want context cancellation", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want none after cancellation", calls)
	}
}

func TestServiceRejectsInvalidProviderConfiguration(t *testing.T) {
	service := newService(&http.Client{}, getenv(map[string]string{"SEARXNG_URL": "not-a-url"}), providerOrder())
	available, err := service.Available()
	if err == nil || available || !strings.Contains(err.Error(), "SEARXNG_URL") {
		t.Fatalf("Available() = %t, %v, want invalid configuration error", available, err)
	}
}

func TestProviderClientRejectsRedirects(t *testing.T) {
	client := newProviderClient()
	request, err := http.NewRequest(http.MethodGet, "https://api.example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("provider client accepted a redirect")
	}
}

func TestToolSpillsLargeResultToSandboxFile(t *testing.T) {
	service := newService(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"web":{"results":[` + strings.Repeat(`{"title":"title","url":"https://example.com/","description":"`+strings.Repeat("x", maxSnippetRunes+1)+`"},`, 9) + `{"title":"title","url":"https://example.com/","description":"` + strings.Repeat("x", maxSnippetRunes+1) + `"}]}}`))}, nil
	})}, getenv(map[string]string{"BRAVE_SEARCH_API_KEY": "key"}), providerOrder())
	files := &searchFiles{files: map[string][]byte{}}
	tool := testTool(service)
	tool.files = files

	output, err := tool.Execute(toolContext(t), map[string]any{"query": "stella", "limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"spilled"`) || len(files.files) != 1 {
		t.Fatalf("output=%s files=%d, want spilled result", output, len(files.files))
	}
	if !strings.Contains(output, `"truncated":true`) {
		t.Fatalf("output=%s, want truncated marker preserved in spill receipt", output)
	}
	for _, content := range files.files {
		if !strings.Contains(string(content), `"results"`) {
			t.Fatalf("stored result missing complete payload")
		}
	}
}

func TestToolRejectsUndeclaredInput(t *testing.T) {
	service := newService(&http.Client{}, getenv(map[string]string{"BRAVE_SEARCH_API_KEY": "key"}), providerOrder())
	_, err := testTool(service).Execute(toolContext(t), map[string]any{
		"query":    "stella",
		"provider": "other",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Execute() error = %v, want strict schema refusal", err)
	}
}

func TestToolUsesAnonymousExaFallback(t *testing.T) {
	var request *http.Request
	service := newService(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		request = req.Clone(req.Context())
		body := `event: message
data: {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Title: Exa result\nURL: https://example.com/exa\nHighlights:\nUseful evidence\n---"}]}}
`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, getenv(nil), providerOrder())

	output, err := testTool(service).Execute(toolContext(t), map[string]any{"query": "stella"})
	if err != nil {
		t.Fatal(err)
	}
	if request == nil || request.URL.String() != exaMCPURL {
		t.Fatalf("request URL = %v, want %s", request, exaMCPURL)
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("Accept") != "application/json, text/event-stream" {
		t.Fatalf("anonymous Exa headers = %#v", request.Header)
	}
	if !strings.Contains(output, `"provider":"exa"`) || !strings.Contains(output, "https://example.com/exa") {
		t.Fatalf("output = %s, want normalized Exa MCP result", output)
	}
}

func TestParseExaMCPResults(t *testing.T) {
	results, err := parseExaMCPResults("Title: First\nURL: https://example.com/first\nHighlights:\nTitle: remains content\n---\n\nTitle: Second\nURL: https://example.com/second\nText: second snippet")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Snippet != "Title: remains content" || results[1].Snippet != "second snippet" {
		t.Fatalf("results = %#v", results)
	}
}

func TestSearchBoundsProviderFields(t *testing.T) {
	service := newService(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"web":{"results":[{"title":"` + strings.Repeat("t", maxTitleRunes+1) + `","url":"https://example.com/","description":"` + strings.Repeat("s", maxSnippetRunes+1) + `"}]}}`)),
		}, nil
	})}, getenv(map[string]string{"BRAVE_SEARCH_API_KEY": "key"}), providerOrder())

	result, err := service.search(t.Context(), "stella", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || !result.Results[0].Truncated || !result.Truncated {
		t.Fatalf("result = %#v, want bounded result fields", result)
	}
	if got := len([]rune(result.Results[0].Title)); got != maxTitleRunes {
		t.Errorf("title rune length = %d, want %d", got, maxTitleRunes)
	}
}
