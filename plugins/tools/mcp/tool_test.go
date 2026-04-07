package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	pkgmcp "github.com/vaayne/anna/pkg/mcp"
)

type fakeSession struct {
	tools      []*officialmcp.Tool
	callResult *officialmcp.CallToolResult
	waitCh     chan error
}

func newFakeSession(tools ...*officialmcp.Tool) *fakeSession {
	return &fakeSession{tools: tools, waitCh: make(chan error, 1)}
}

func (s *fakeSession) Close() error {
	select {
	case s.waitCh <- context.Canceled:
	default:
	}
	return nil
}

func (s *fakeSession) Wait() error {
	return <-s.waitCh
}

func (s *fakeSession) ListTools(context.Context, *officialmcp.ListToolsParams) (*officialmcp.ListToolsResult, error) {
	return &officialmcp.ListToolsResult{Tools: s.tools}, nil
}

func (s *fakeSession) CallTool(context.Context, *officialmcp.CallToolParams) (*officialmcp.CallToolResult, error) {
	return s.callResult, nil
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestToolListAndGet(t *testing.T) {
	mgr := pkgmcp.NewManager()
	mgr.Configure(pkgmcp.Config{Servers: []pkgmcp.ServerConfig{{Name: "github", Enabled: true, Transport: pkgmcp.TransportStdio, Command: "cmd"}}}, true)
	mgr.RegisterTool("github", "search_repos", "Search Repos", "Search repositories", map[string]any{"type": "object"}, map[string]any{"type": "object"}, map[string]any{"read_only_hint": true})
	tool := New(mgr)

	listJSON, err := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed []map[string]any
	if err := json.Unmarshal([]byte(listJSON), &listed); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(listed) != 1 || listed[0]["server_name"] != "github" {
		t.Fatalf("unexpected list: %#v", listed)
	}

	getJSON, err := tool.Execute(context.Background(), map[string]any{"action": "get", "id": listed[0]["id"]})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(getJSON), &detail); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if detail["tool_name"] != "search_repos" {
		t.Fatalf("unexpected detail: %#v", detail)
	}
}

func TestToolExec(t *testing.T) {
	mgr := pkgmcp.NewManager()
	sess := newFakeSession(&officialmcp.Tool{Name: "hello", Description: "Hello"})
	sess.callResult = &officialmcp.CallToolResult{Content: []officialmcp.Content{&officialmcp.TextContent{Text: "hi"}}}
	mgr.SetDial(func(context.Context, pkgmcp.ServerConfig) (pkgmcp.Session, error) { return sess, nil })
	mgr.Reconcile(context.Background(), pkgmcp.Config{Servers: []pkgmcp.ServerConfig{{Name: "demo", Enabled: true, Transport: pkgmcp.TransportStdio, Command: "cmd"}}}, true)
	waitFor(t, time.Second, func() bool { return len(mgr.ValidTools()) == 1 })
	proxy := New(mgr)
	toolID := mgr.ValidTools()[0].ID

	payload, err := proxy.Execute(context.Background(), map[string]any{"action": "exec", "id": toolID, "args": map[string]any{"name": "V"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("unmarshal exec: %v", err)
	}
	if result["tool_name"] != "hello" || result["ok"] != true {
		t.Fatalf("unexpected exec result: %#v", result)
	}
}

func TestToolValidation(t *testing.T) {
	proxy := New(pkgmcp.NewManager())
	if _, err := proxy.Execute(context.Background(), map[string]any{"action": "get"}); err == nil {
		t.Fatal("expected get id validation error")
	}
	if _, err := proxy.Execute(context.Background(), map[string]any{"action": "exec"}); err == nil {
		t.Fatal("expected exec id validation error")
	}
	if _, err := proxy.Execute(context.Background(), map[string]any{"action": "wat"}); err == nil {
		t.Fatal("expected unsupported action error")
	}
}
