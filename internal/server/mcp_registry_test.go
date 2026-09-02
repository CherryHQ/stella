package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeCatalog is a scripted mcp.Catalog for handler tests.
type fakeCatalog struct {
	page       mcp.CatalogPage
	err        error
	gotQ       string
	gotSize    int
	gotToken   string
	gotSource  string
	gotID      string
	getErr     error
	getErrType error
}

func (f *fakeCatalog) Search(_ context.Context, q string, pageSize int, pageToken string) (mcp.CatalogPage, error) {
	f.gotQ, f.gotSize, f.gotToken = q, pageSize, pageToken
	if f.err != nil {
		return mcp.CatalogPage{}, f.err
	}
	return f.page, nil
}

func (f *fakeCatalog) Get(_ context.Context, source, id string) (mcp.CatalogServer, error) {
	f.gotSource, f.gotID = source, id
	if f.getErrType != nil {
		return mcp.CatalogServer{}, f.getErrType
	}
	if f.getErr != nil {
		return mcp.CatalogServer{}, f.getErr
	}
	if len(f.page.Servers) == 0 {
		return mcp.CatalogServer{}, errNotFoundCatalog{}
	}
	return f.page.Servers[0], nil
}

type errNotFoundCatalog struct{}

func (errNotFoundCatalog) Error() string { return "mcp: registry server not found" }

func rateLimitErr(retryAfter string) error {
	return &mcp.RegistryRateLimitError{RetryAfter: retryAfter}
}

func setupRegistryEnv(t *testing.T, catalog mcp.Catalog) *testEnv {
	t.Helper()
	env := setupAdmin(t)
	env.rebuild(t, func(d *server.Deps) {
		d.MCPCatalog = catalog
	})
	return env
}

func TestRegistryListClampsPageSize(t *testing.T) {
	catalog := &fakeCatalog{page: mcp.CatalogPage{Servers: []mcp.CatalogServer{
		{Source: "official", ID: "com.notion/mcp", Name: "com.notion/mcp", URL: "https://mcp.notion.com/mcp", Transport: "streamable_http", Auth: "none"},
	}}}
	env := setupRegistryEnv(t, catalog)

	for _, tc := range []struct{ request, want int }{
		{-1, 20}, {0, 20}, {5, 5}, {500, 50},
	} {
		rr := doRequest(t, env, http.MethodGet, "/api/mcp/registry/servers?page_size=-1", nil)
		_ = rr
		_ = tc
		break
	}
	// One request per clamp case, checked via the recorded page size.
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", 20},
		{"page_size=0", 20},
		{"page_size=5", 5},
		{"page_size=500", 50},
	} {
		rr := doRequest(t, env, http.MethodGet, "/api/mcp/registry/servers?"+tc.query, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
		}
		if catalog.gotSize != tc.want {
			t.Fatalf("page_size %q -> catalog received %d, want %d", tc.query, catalog.gotSize, tc.want)
		}
	}
}

func TestRegistryListReturnsServersAndToken(t *testing.T) {
	catalog := &fakeCatalog{page: mcp.CatalogPage{
		Servers: []mcp.CatalogServer{{
			Source: "official", ID: "com.smithery/x", Name: "com.smithery/x",
			URL: "https://x.example/mcp", Transport: "streamable_http", Auth: "bearer",
			Headers: []mcp.CatalogHeader{{Name: "Authorization", Template: "Bearer {key}", Required: true, Secret: true}},
		}},
		NextPageToken: "official:cursor-2",
	}}
	env := setupRegistryEnv(t, catalog)

	rr := doRequest(t, env, http.MethodGet, "/api/mcp/registry/servers?q=smith&page_size=20&page_token=official%3Acursor-1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if catalog.gotQ != "smith" || catalog.gotToken != "official:cursor-1" {
		t.Fatalf("params = q %q token %q", catalog.gotQ, catalog.gotToken)
	}
	var got struct {
		Servers []struct {
			ID     string `json:"id"`
			Auth   string `json:"auth"`
			Header []struct {
				Template string `json:"template"`
			} `json:"headers"`
		} `json:"servers"`
		NextPageToken *string `json:"next_page_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Servers) != 1 || got.Servers[0].Auth != "bearer" {
		t.Fatalf("servers = %#v", got.Servers)
	}
	if got.Servers[0].Header[0].Template != "Bearer {key}" {
		t.Fatalf("header template = %#v", got.Servers[0].Header)
	}
	if got.NextPageToken == nil || *got.NextPageToken != "official:cursor-2" {
		t.Fatalf("next_page_token = %v", got.NextPageToken)
	}
}

func TestRegistryRateLimitedMapsTo503WithRetryAfter(t *testing.T) {
	catalog := &fakeCatalog{err: rateLimitErr("30")}
	env := setupRegistryEnv(t, catalog)

	rr := doRequest(t, env, http.MethodGet, "/api/mcp/registry/servers", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want 30", got)
	}
}

func TestRegistryDetailNotFound(t *testing.T) {
	catalog := &fakeCatalog{getErr: errNotFoundCatalog{}}
	env := setupRegistryEnv(t, catalog)

	rr := doRequest(t, env, http.MethodGet, "/api/mcp/registry/servers/official/com.missing%2Fmcp", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestRegistryInstallDuplicateURLConflicts(t *testing.T) {
	env := setupAdmin(t)
	env.rebuild(t, func(d *server.Deps) {
		d.MCP = mcp.NewServiceForPool(env.db, nil, nil)
		d.MCPAccess = mcp.NewAccess(d.MCP, nil, nil)
	})

	first := doRequest(t, env, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name": "one", "url": "https://mcp.example.com", "scope": "user",
		"source": "official", "source_id": "com.notion/mcp", "source_version": "1.0.1",
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status = %d (body: %s)", first.Code, first.Body.String())
	}
	second := doRequest(t, env, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name": "two", "url": "https://mcp.example.com", "scope": "user",
	})
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want 409 (body: %s)", second.Code, second.Body.String())
	}
	// The provenance landed on the installed row's metadata.
	q := sqlc.New(env.db)
	rows, err := q.ListMCPServersByScope(context.Background(), sqlc.ListMCPServersByScopeParams{Scope: mcp.ScopeUser, UserID: pgnull.Text(env.adminUser.ID)})
	if err != nil || len(rows) == 0 {
		t.Fatalf("list rows: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(rows[0].Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	registryMeta, ok := metadata["registry"].(map[string]any)
	if !ok || registryMeta["source"] != "official" || registryMeta["id"] != "com.notion/mcp" {
		t.Fatalf("metadata.registry = %#v", metadata["registry"])
	}
	if _, ok := registryMeta["installed_at"]; !ok {
		t.Fatal("installed_at missing from registry provenance")
	}
}
