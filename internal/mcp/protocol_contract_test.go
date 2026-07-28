package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// contractBearerTransport injects the decrypted fixture credential without
// weakening the production client's loopback/private-address SSRF guard.
// Production header injection and endpoint rejection remain covered separately.
type contractBearerTransport struct {
	base   http.RoundTripper
	bearer string
}

func (t contractBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.bearer)
	return t.base.RoundTrip(clone)
}

type mcpProtocolFixture struct {
	server   *httptest.Server
	handler  http.Handler
	token    string
	mu       sync.Mutex
	methods  map[string]int
	requests int
}

func newMCPProtocolFixture(t *testing.T, token string) *mcpProtocolFixture {
	t.Helper()

	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "stella-release-contract", Version: "1.0.0"},
		nil,
	)
	server.AddTool(
		&mcpsdk.Tool{
			Name:        "echo",
			Description: "Echo one string through the real MCP protocol.",
			InputSchema: json.RawMessage(`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}},"additionalProperties":false}`),
		},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, fmt.Errorf("decode echo arguments: %w", err)
			}
			if args.Message == "" {
				return &mcpsdk.CallToolResult{
					IsError: true,
					Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "message is required"}},
				}, nil
			}
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + args.Message}},
			}, nil
		},
	)
	server.AddTool(
		&mcpsdk.Tool{
			Name:        "fail",
			Description: "Return an MCP tool-level error.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{
				IsError: true,
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "fixture tool failure"}},
			}, nil
		},
	)
	server.AddTool(
		&mcpsdk.Tool{
			Name:        "slow",
			Description: "Wait until the caller cancels the request.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return &mcpsdk.CallToolResult{
					Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "unexpected completion"}},
				}, nil
			}
		},
	)

	fixture := &mcpProtocolFixture{
		handler: mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
			return server
		}, nil),
		token:   token,
		methods: map[string]int{},
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

// serveHTTP makes the fixture fail closed on every protocol surface. A wrong
// path, method, or bearer token is rejected before the MCP SDK sees the request.
func (f *mcpProtocolFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.methods[r.Method]++
	f.mu.Unlock()

	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodDelete:
		f.handler.ServeHTTP(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *mcpProtocolFixture) connect(
	ctx context.Context,
	reg Registration,
	bearer string,
) (mcpClient, error) {
	httpClient := &http.Client{
		Transport: contractBearerTransport{
			base:   http.DefaultTransport,
			bearer: bearer,
		},
		Timeout: 5 * time.Second,
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   reg.URL,
		HTTPClient: httpClient,
	}
	session, err := mcpsdk.NewClient(clientImpl, nil).Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect protocol fixture: %w", err)
	}
	return &Client{session: session}, nil
}

func (f *mcpProtocolFixture) requestSnapshot() (int, map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	methods := make(map[string]int, len(f.methods))
	maps.Copy(methods, f.methods)
	return f.requests, methods
}

// TestMCPStreamableHTTPProtocolContract proves the real SDK initialize,
// tools/list, tools/call, tool errors, cancellation, cache, and reconnect path.
// The in-process server is deterministic while still speaking MCP over HTTP.
func TestMCPStreamableHTTPProtocolContract(t *testing.T) {
	const (
		userID = "mcp-contract-user"
		token  = "mcp-contract-bearer"
	)
	fixture := newMCPProtocolFixture(t, token)
	now := time.Unix(1_000, 0).UTC()
	db := newFakeDB()
	db.forCtx = []sqlc.McpServer{{
		ID:            "mcp-contract-server",
		Scope:         ScopeUser,
		UserID:        pgnull.Text(userID),
		Name:          "fixture",
		Url:           fixture.server.URL + "/mcp",
		Transport:     TransportStreamableHTTP,
		AuthType:      AuthTypeBearer,
		CredentialRef: "MCP_TOKEN_CONTRACT",
		Enabled:       true,
		UpdatedAt:     now,
	}}
	vlt := newFakeVault()
	vlt.stored[vaultKey(ScopeUser, userID, "", "MCP_TOKEN_CONTRACT")] = token

	provider := NewToolProvider(NewService(db, vlt))
	provider.now = func() time.Time { return now }
	provider.ttl = time.Minute
	var connects atomic.Int32
	provider.connect = func(ctx context.Context, reg Registration, bearer string) (mcpClient, error) {
		connects.Add(1)
		return fixture.connect(ctx, reg, bearer)
	}

	first := toolsByDefinitionName(provider.ToolsForContext(context.Background(), userID, ""))
	if len(first) != 3 {
		t.Fatalf("discovered %d tools, want 3", len(first))
	}
	if got := first["mcp__fixture__echo"].Definition().InputSchema["type"]; got != "object" {
		t.Fatalf("echo input schema type = %v, want object", got)
	}
	output, err := first["mcp__fixture__echo"].Execute(
		context.Background(),
		map[string]any{"message": "hello"},
	)
	if err != nil {
		t.Fatalf("execute echo: %v", err)
	}
	if output != "echo:hello" {
		t.Fatalf("echo output = %q, want %q", output, "echo:hello")
	}

	if _, err := first["mcp__fixture__fail"].Execute(context.Background(), map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "fixture tool failure") {
		t.Fatalf("tool-level error = %v, want fixture failure", err)
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	started := time.Now()
	_, timeoutErr := first["mcp__fixture__slow"].Execute(timeoutCtx, map[string]any{})
	cancel()
	if timeoutErr == nil {
		t.Fatal("slow tool succeeded after its context deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("slow tool cancellation took %s, want under 1s", elapsed)
	}
	closeContractTools(t, first)

	// The second discovery uses the cached tool list, then lazily establishes a
	// fresh protocol session when the cached proxy is invoked.
	second := toolsByDefinitionName(provider.ToolsForContext(context.Background(), userID, ""))
	if got := connects.Load(); got != 1 {
		t.Fatalf("connections after cached discovery = %d, want 1", got)
	}
	output, err = second["mcp__fixture__echo"].Execute(
		context.Background(),
		map[string]any{"message": "reconnected"},
	)
	if err != nil {
		t.Fatalf("execute echo after reconnect: %v", err)
	}
	if output != "echo:reconnected" {
		t.Fatalf("reconnected output = %q, want %q", output, "echo:reconnected")
	}
	if got := connects.Load(); got != 2 {
		t.Fatalf("connections after lazy reconnect = %d, want 2", got)
	}
	closeContractTools(t, second)

	requests, methods := fixture.requestSnapshot()
	if requests == 0 || methods[http.MethodPost] == 0 {
		t.Fatalf("fixture requests = %d, methods = %v; want protocol POST traffic", requests, methods)
	}
	for method := range methods {
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodDelete:
		default:
			t.Fatalf("unexpected MCP method %q", method)
		}
	}
}

func toolsByDefinitionName(tools []pkgtools.Tool) map[string]pkgtools.Tool {
	out := make(map[string]pkgtools.Tool, len(tools))
	for _, tool := range tools {
		out[tool.Definition().Name] = tool
	}
	return out
}

func closeContractTools(t *testing.T, tools map[string]pkgtools.Tool) {
	t.Helper()
	for name, tool := range tools {
		closer, ok := tool.(interface{ Close() error })
		if !ok {
			t.Fatalf("tool %s does not expose lifecycle cleanup", name)
		}
		if err := closer.Close(); err != nil {
			t.Errorf("close tool %s: %v", name, err)
		}
	}
}
