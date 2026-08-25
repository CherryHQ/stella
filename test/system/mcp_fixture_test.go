//go:build system

package system

import (
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/test/testbed/mcpfixture"
)

// testbedMCPFixture is the local Streamable HTTP catalog used by the testbed
// specialized-tools lane. It contains only public tool names and generic
// schemas, so test failures can report a count and name digest without logging
// schemas, arguments, or results.
type testbedMCPFixture struct {
	listener net.Listener
	server   *http.Server
	routeKey []byte
}

func newTestbedMCPFixture(t *testing.T) *testbedMCPFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen testbed MCP fixture: %v", err)
	}
	routeKey := make([]byte, 32)
	if _, err := rand.Read(routeKey); err != nil {
		_ = listener.Close()
		t.Fatalf("generate testbed MCP route key: %v", err)
	}
	mcpServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stella-testbed-fixture", Version: "1"}, nil)
	for _, name := range mcpfixture.ToolNames() {
		toolName := name
		mcpsdk.AddTool(mcpServer, &mcpsdk.Tool{
			Name:        toolName,
			Description: "Deterministic Stella evaluation fixture tool.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": true},
		}, func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "fixture"}}}, nil, nil
		})
	}
	fixture := &testbedMCPFixture{
		listener: listener,
		routeKey: routeKey,
		server:   &http.Server{Handler: mcpfixture.NewStreamableHTTPHandler(routeKey, mcpServer, nil)},
	}
	go func() { _ = fixture.server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fixture.server.Shutdown(ctx); err != nil {
			t.Errorf("stop testbed MCP fixture: %v", err)
		}
	})
	return fixture
}

func (f *testbedMCPFixture) authority() string { return f.listener.Addr().String() }

func (f *testbedMCPFixture) routeForTrial(trial string) (string, error) {
	return mcpfixture.RouteForTrial(f.routeKey, trial)
}
