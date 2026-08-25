package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	stellamcp "github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/test/testbed/mcpfixture"
)

const (
	fixtureToolCount        = mcpfixture.ToolCount
	fixtureConfigFilename   = "testbed-mcp-fixture.json"
	fixtureRegistrationName = mcpfixture.RegistrationName
)

type fixtureLedgerEntry struct {
	RouteID              string `json:"route_id"`
	Method               string `json:"method"`
	Tool                 string `json:"tool,omitempty"`
	Outcome              string `json:"outcome,omitempty"`
	InputMatchesExpected bool   `json:"input_matches_expected,omitempty"`
	DependsOnPrevious    bool   `json:"depends_on_previous,omitempty"`
	StepDigest           string `json:"step_digest,omitempty"`
	At                   string `json:"at"`
}

type fixtureRoute struct {
	id      string
	entries []fixtureLedgerEntry
}

// fixtureListener is owned by the testbed supervisor, never the agent or
// evaluator. The policy descriptor only authorizes this listener's exact
// address; a route is still an opaque per-trial capability.
type fixtureListener struct {
	listener net.Listener
	routeKey []byte
	server   *http.Server
	mcp      http.Handler
	mu       sync.Mutex
	routes   map[string]*fixtureRoute
}

func newFixtureListener() (*fixtureListener, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		_ = listener.Close()
		return nil, err
	}
	f := &fixtureListener{listener: listener, routeKey: key, routes: map[string]*fixtureRoute{}}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stella-testbed-fixture", Version: "1"}, nil)
	for _, name := range fixtureToolNames() {
		toolName := name
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        toolName,
			Description: "Deterministic Stella evaluation fixture tool.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": true},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, args map[string]any) (*mcpsdk.CallToolResult, any, error) {
			result, meta, err := f.call(toolName, args)
			f.recordTool(ctx, toolName, args, result != nil && !result.IsError && err == nil)
			return result, meta, err
		})
	}
	f.mcp = mcpfixture.NewStreamableHTTPHandler(f.routeKey, server, func(route, method string) {
		f.recordRouteMethod(route, method)
	})
	f.server = &http.Server{Handler: f.mcp}
	go func() { _ = f.server.Serve(listener) }()
	return f, nil
}

func (f *fixtureListener) Close(ctx context.Context) error { return f.server.Shutdown(ctx) }

func (f *fixtureListener) Authority() string { return f.listener.Addr().String() }

func (f *fixtureListener) Descriptor() ([]byte, error) {
	return stellamcp.FixturePolicyDescriptor(f.Authority())
}

type fixtureConfig struct {
	Version              int    `json:"version"`
	Authority            string `json:"authority"`
	RouteKey             string `json:"route_key"`
	CleanupSocket        string `json:"cleanup_socket"`
	CatalogDigest        string `json:"catalog_digest"`
	ArticleCanonicalURL  string `json:"article_canonical_url"`
	ArticleTitle         string `json:"article_title"`
	ArticleContentDigest string `json:"article_content_digest"`
	FixturePlanDigest    string `json:"fixture_plan_digest"`
	FixturePlanSeed      string `json:"fixture_plan_seed"`
}

const (
	fixtureArticleCanonicalURL = "https://fixture.invalid/article/amber-meadow"
	fixtureArticleTitle        = "Amber Meadow"
	fixtureArticleContent      = "amber meadow"
)

func (f *fixtureListener) writeConfig(home, cleanupSocket, fixturePlanSeed string) (string, error) {
	payload, err := json.Marshal(fixtureConfig{
		Version: 1, Authority: "http://" + f.Authority(), RouteKey: base64.RawURLEncoding.EncodeToString(f.routeKey), CleanupSocket: cleanupSocket,
		CatalogDigest: fixtureCatalogDigest(), ArticleCanonicalURL: fixtureArticleCanonicalURL, ArticleTitle: fixtureArticleTitle, ArticleContentDigest: fixtureContentDigest(fixtureArticleContent), FixturePlanDigest: fixturePlanDigest(), FixturePlanSeed: fixturePlanSeed,
	})
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, fixtureConfigFilename)
	tmp, err := os.CreateTemp(home, ".mcp-fixture-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(payload, '\n')); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func fixtureCatalogDigest() string { return mcpfixture.CatalogDigest() }

func fixtureContentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fixturePlanDigest() string {
	plan := strings.Join([]string{fixtureCatalogDigest(), fixtureArticleCanonicalURL, fixtureArticleTitle, fixtureContentDigest(fixtureArticleContent)}, "\n")
	sum := sha256.Sum256([]byte(plan))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fixtureToolNames() []string { return mcpfixture.ToolNames() }

func (f *fixtureListener) recordRouteMethod(route, method string) {
	f.record(f.ensureRoute(route), method, "")
}

func (f *fixtureListener) ensureRoute(route string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing := f.routes[route]; existing != nil {
		return existing.id
	}
	id := randomFixtureID()
	f.routes[route] = &fixtureRoute{id: id}
	return id
}

func (f *fixtureListener) recordTool(ctx context.Context, tool string, args map[string]any, success bool) {
	route := mcpfixture.RouteFromContext(ctx)
	if route == "" {
		return
	}
	routeID := f.ensureRoute(route)
	expected := tool == "lookup_brief" || (tool == "transform_brief" && args["input"] == "stage-one: river") || (tool == "commit_brief" && args["input"] == "canonical_url="+fixtureArticleCanonicalURL+"\ntitle="+fixtureArticleTitle+"\ncontent="+fixtureArticleContent)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, route := range f.routes {
		if route.id != routeID {
			continue
		}
		previous := tool == "lookup_brief"
		for _, entry := range route.entries {
			if entry.Outcome != "success" {
				continue
			}
			if tool == "transform_brief" && entry.Tool == "lookup_brief" {
				previous = true
			}
			if tool == "commit_brief" && entry.Tool == "transform_brief" {
				previous = true
			}
		}
		outcome := "error"
		if success {
			outcome = "success"
		}
		sum := sha256.Sum256([]byte(tool))
		route.entries = append(route.entries, fixtureLedgerEntry{RouteID: routeID, Method: "tools/call", Tool: tool, Outcome: outcome, InputMatchesExpected: expected, DependsOnPrevious: previous, StepDigest: "sha256:" + hex.EncodeToString(sum[:]), At: time.Now().UTC().Format(time.RFC3339Nano)})
		return
	}
}

func (f *fixtureListener) record(routeID, method, tool string, outcome ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, route := range f.routes {
		if route.id == routeID {
			entry := fixtureLedgerEntry{RouteID: routeID, Method: method, Tool: tool, At: time.Now().UTC().Format(time.RFC3339Nano)}
			if len(outcome) > 0 {
				entry.Outcome = outcome[0]
			}
			route.entries = append(route.entries, entry)
			return
		}
	}
}

func (f *fixtureListener) routeForTrial(trial string) (string, error) {
	return mcpfixture.RouteForTrial(f.routeKey, trial)
}

func (f *fixtureListener) Ledger(route string) ([]fixtureLedgerEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.routes[route]
	if r == nil {
		return nil, false
	}
	return append([]fixtureLedgerEntry(nil), r.entries...), true
}

func (f *fixtureListener) call(name string, args map[string]any) (*mcpsdk.CallToolResult, any, error) {
	text := "fixture decoy"
	switch name {
	case "lookup_brief":
		text = "stage-one: river"
	case "transform_brief":
		if args["input"] != "stage-one: river" {
			return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "missing lookup input"}}}, nil, nil
		}
		text = "canonical_url=" + fixtureArticleCanonicalURL + "\ntitle=" + fixtureArticleTitle + "\ncontent=" + fixtureArticleContent
	case "commit_brief":
		if args["input"] != "canonical_url="+fixtureArticleCanonicalURL+"\ntitle="+fixtureArticleTitle+"\ncontent="+fixtureArticleContent {
			return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "missing transform input"}}}, nil, nil
		}
		text = "committed"
	}
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}}, nil, nil
}

func randomFixtureID() string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("testbed fixture random source failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}
