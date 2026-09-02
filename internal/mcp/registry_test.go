package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInferAuthClassification(t *testing.T) {
	bearer := CatalogHeader{Name: "Authorization", Template: "Bearer {smithery_api_key}", Required: true, Secret: true}
	for _, tc := range []struct {
		name    string
		headers []CatalogHeader
		want    string
	}{
		{"no headers", nil, registryAuthNone},
		{"bearer", []CatalogHeader{bearer}, registryAuthBearer},
		{"authorization case-insensitive", []CatalogHeader{{Name: "authorization", Template: "Bearer k", Required: true}}, registryAuthBearer},
		{"non-bearer authorization", []CatalogHeader{{Name: "Authorization", Template: "ApiKey k", Required: true}}, registryAuthUnsupported},
		{"extra required header", []CatalogHeader{bearer, {Name: "X-Tenant", Required: true}}, registryAuthUnsupported},
		{"optional custom header only", []CatalogHeader{{Name: "X-Org", Required: false}}, registryAuthNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferAuth(tc.headers); got != tc.want {
				t.Fatalf("inferAuth = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPageTokenRoundTrip(t *testing.T) {
	if _, _, err := decodePageToken("other:abc"); err == nil {
		t.Fatal("foreign source token must be rejected")
	}
	source, cursor, err := decodePageToken("official:com.notion/mcp:1.0.1")
	if err != nil || source != RegistrySourceOfficial || cursor != "com.notion/mcp:1.0.1" {
		t.Fatalf("decode = %q %q %v", source, cursor, err)
	}
	if got := encodePageToken("abc"); got != "official:abc" {
		t.Fatalf("encode = %q", got)
	}
	if got := encodePageToken(""); got != "" {
		t.Fatalf("empty cursor must encode to an empty token, got %q", got)
	}
}

// scriptedRegistry serves canned upstream pages with per-page call counting.
func scriptedRegistry(t *testing.T, pages []string, status int) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		idx := calls - 1
		if idx >= len(pages) {
			idx = len(pages) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pages[idx]))
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

const pageOne = `{"servers":[
 {"server":{"name":"com.notion/mcp","description":"Notion","version":"1.0.1","remotes":[{"type":"streamable-http","url":"https://mcp.notion.com/mcp"}]},"_meta":{"io.modelcontextprotocol.registry/official":{"isLatest":true}}},
 {"server":{"name":"com.sse/only","description":"SSE only","version":"1.0.0","remotes":[{"type":"sse","url":"https://sse.example/sse"}]}},
 {"server":{"name":"com.smithery/x","description":"Bearer","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://x.example/mcp","headers":[{"name":"Authorization","value":"Bearer {key}","isRequired":true,"isSecret":true}]}]}}
],"metadata":{"nextCursor":"cursor-2"}}`

const pageTwo = `{"servers":[
 {"server":{"name":"com.custom/y","description":"Custom header","version":"2.0.0","remotes":[{"type":"streamable-http","url":"https://y.example/mcp","headers":[{"name":"X-Tenant","value":"{tenant}","isRequired":true}]}]}},
 {"server":{"name":"com.stdio/only","description":"stdio","version":"1.0.0","packages":[{"registryType":"pypi","identifier":"x","version":"1","transport":{"type":"stdio"}}]}}
],"metadata":{"nextCursor":""}}`

func TestSearchFiltersAndPaginates(t *testing.T) {
	ts, calls := scriptedRegistry(t, []string{pageOne, pageTwo}, 0)
	t.Setenv("STELLA_MCP_REGISTRY_URL", ts.URL)
	catalog := NewOfficialCatalog()

	page, err := catalog.Search(context.Background(), "", 2, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Upstream page 1 fills the budget with 2 streamable entries; the sse-only
	// entry is filtered out client-side.
	if len(page.Servers) != 2 {
		t.Fatalf("servers = %d, want 2 (sse and stdio entries filtered out)", len(page.Servers))
	}
	if *calls != 1 {
		t.Fatalf("upstream pages = %d, want 1", *calls)
	}
	if page.Servers[0].Auth != registryAuthNone || page.Servers[1].Auth != registryAuthBearer {
		t.Fatalf("auth inference = %s/%s", page.Servers[0].Auth, page.Servers[1].Auth)
	}
	if page.NextPageToken == "" {
		t.Fatal("next page token missing")
	}
	// Round-trip the token: the next search resumes at the stored cursor.
	page2, err := catalog.Search(context.Background(), "", 2, page.NextPageToken)
	if err != nil {
		t.Fatalf("Search(page 2): %v", err)
	}
	if len(page2.Servers) != 1 || page2.Servers[0].Auth != registryAuthUnsupported {
		t.Fatalf("page 2 = %#v, want the custom-header entry as unsupported", page2.Servers)
	}
	if page2.NextPageToken != "" {
		t.Fatalf("exhausted upstream must return an empty token, got %q", page2.NextPageToken)
	}
	if !contains(page2.Servers[0].Headers[0].Template, "{tenant}") {
		t.Fatalf("header template = %q", page2.Servers[0].Headers[0].Template)
	}
}

func TestSearchRateLimitedIsTyped(t *testing.T) {
	ts, _ := scriptedRegistry(t, nil, http.StatusTooManyRequests)
	t.Setenv("STELLA_MCP_REGISTRY_URL", ts.URL)
	catalog := NewOfficialCatalog()
	_, err := catalog.Search(context.Background(), "", 20, "")
	var rateLimited *RegistryRateLimitError
	if !asRateLimit(err, &rateLimited) {
		t.Fatalf("error = %v, want RegistryRateLimitError", err)
	}
}

func asRateLimit(err error, target **RegistryRateLimitError) bool {
	rl := &RegistryRateLimitError{}
	if errors.As(err, &rl) {
		*target = rl
		return true
	}
	return false
}

func TestSearchMalformedJSONIsError(t *testing.T) {
	ts, _ := scriptedRegistry(t, []string{"{not json"}, 0)
	t.Setenv("STELLA_MCP_REGISTRY_URL", ts.URL)
	if _, err := NewOfficialCatalog().Search(context.Background(), "", 20, ""); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("error = %v, want a decode failure", err)
	}
}

func TestGetDetailPrefersLatest(t *testing.T) {
	ts, _ := scriptedRegistry(t, []string{`{"servers":[
	 {"server":{"name":"com.notion/mcp","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://old.example/mcp"}]},"_meta":{"io.modelcontextprotocol.registry/official":{"isLatest":false}}},
	 {"server":{"name":"com.notion/mcp","version":"1.0.1","remotes":[{"type":"streamable-http","url":"https://new.example/mcp"}]},"_meta":{"io.modelcontextprotocol.registry/official":{"isLatest":true}}}
	]}`}, 0)
	t.Setenv("STELLA_MCP_REGISTRY_URL", ts.URL)
	srv, err := NewOfficialCatalog().Get(context.Background(), RegistrySourceOfficial, "com.notion/mcp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if srv.Version != "1.0.1" || srv.URL != "https://new.example/mcp" {
		t.Fatalf("detail = %s %s, want the latest version", srv.Version, srv.URL)
	}
	if _, err := NewOfficialCatalog().Get(context.Background(), "elsewhere", "x"); err == nil {
		t.Fatal("unknown source must be rejected")
	}
}
