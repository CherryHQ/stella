package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func testTool(service *Service) *Tool { return NewTool(service, ActionTools()[0]) }

func toolContext(t *testing.T) context.Context {
	t.Helper()
	return authz.WithAgentID(authz.WithUserID(t.Context(), "user-1"), "agent-1")
}

func TestToolSearch(t *testing.T) {
	var request *http.Request
	service := newService("brave-secret", &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
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
	})})

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

func TestToolRejectsUndeclaredInput(t *testing.T) {
	service := newService("key", &http.Client{})
	_, err := testTool(service).Execute(toolContext(t), map[string]any{
		"query":    "stella",
		"provider": "other",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Execute() error = %v, want strict schema refusal", err)
	}
}

func TestToolRequiresConfiguredProvider(t *testing.T) {
	_, err := testTool(newService("", &http.Client{})).Execute(toolContext(t), map[string]any{"query": "stella"})
	if err == nil || !strings.Contains(err.Error(), "STELLA_BRAVE_SEARCH_API_KEY") {
		t.Fatalf("Execute() error = %v, want configuration guidance", err)
	}
}

func TestSearchBoundsProviderFields(t *testing.T) {
	service := newService("key", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"web":{"results":[{"title":"` + strings.Repeat("t", maxTitleRunes+1) + `","url":"https://example.com/","description":"` + strings.Repeat("s", maxSnippetRunes+1) + `"}]}}`)),
		}, nil
	})})

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
