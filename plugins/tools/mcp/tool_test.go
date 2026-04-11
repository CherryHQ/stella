package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolListAndGet(t *testing.T) {
	mgr := NewManager()
	mgr.AddTool("github", "search_repos", "Search Repos", "Search repositories", map[string]any{"type": "object"}, map[string]any{"type": "object"}, map[string]any{"read_only_hint": true})
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
	mgr := NewManager()
	sess := newFakeSession(&officialmcp.Tool{Name: "hello", Description: "Hello"})
	sess.callResult = &officialmcp.CallToolResult{Content: []officialmcp.Content{&officialmcp.TextContent{Text: "hi"}}}
	mgr.SetDial(func(context.Context, ServerConfig) (Session, error) { return sess, nil })
	mgr.Reconcile(context.Background(), Config{Servers: []ServerConfig{{Name: "demo", Enabled: true, Transport: TransportStdio, Command: "cmd"}}}, true)
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
	proxy := New(NewManager())
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
