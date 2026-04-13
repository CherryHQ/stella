package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/sandbox"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeSession struct {
	tools      []*officialmcp.Tool
	callResult *officialmcp.CallToolResult
	callErr    error
	waitCh     chan error
	closed     bool
	mu         sync.Mutex
}

func newFakeSession(tools ...*officialmcp.Tool) *fakeSession {
	return &fakeSession{tools: tools, waitCh: make(chan error, 1)}
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.waitCh <- context.Canceled
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
	if s.callErr != nil {
		return nil, s.callErr
	}
	if s.callResult == nil {
		return &officialmcp.CallToolResult{}, nil
	}
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

func TestManagerReconcileStartsServerAndDiscoversTools(t *testing.T) {
	mgr := NewManager()
	mgr.SetSupervisorConfig(SupervisorConfig{FailureThreshold: 3, BackoffBase: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond})
	sess := newFakeSession(&officialmcp.Tool{Name: "search_repos", Description: "Search repositories", InputSchema: map[string]any{"type": "object"}})
	mgr.SetDial(func(context.Context, ServerConfig, sandbox.Host) (Session, error) {
		return sess, nil
	})
	mgr.SetHost(&stdioHostStub{})

	mgr.Reconcile(context.Background(), Config{Servers: []ServerConfig{{Name: "github", Enabled: true, Transport: TransportStdio, Command: "npx"}}}, true)

	waitFor(t, time.Second, func() bool { return len(mgr.ValidTools()) == 1 })
	tools := mgr.ValidTools()
	if tools[0].ID == "" || tools[0].ServerName != "github" || tools[0].ToolName != "search_repos" {
		t.Fatalf("unexpected tool: %+v", tools[0])
	}
	statuses := mgr.Statuses()
	if len(statuses) != 1 || statuses[0].State != serverStateRunning {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}

	mgr.Reconcile(context.Background(), Config{}, false)
	waitFor(t, time.Second, func() bool { return len(mgr.ValidTools()) == 0 })
}

func TestManagerSuppressesAlwaysFailingServer(t *testing.T) {
	mgr := NewManager()
	mgr.SetSupervisorConfig(SupervisorConfig{FailureThreshold: 2, BackoffBase: 5 * time.Millisecond, BackoffMax: 10 * time.Millisecond})
	mgr.SetDial(func(context.Context, ServerConfig, sandbox.Host) (Session, error) {
		return nil, errors.New("boom")
	})
	mgr.SetHost(&stdioHostStub{})

	mgr.Reconcile(context.Background(), Config{Servers: []ServerConfig{{Name: "broken", Enabled: true, Transport: TransportStdio, Command: "cmd"}}}, true)

	waitFor(t, time.Second, func() bool {
		statuses := mgr.Statuses()
		return len(statuses) == 1 && statuses[0].State == serverStateSuppressed
	})
	statuses := mgr.Statuses()
	if !statuses[0].Suppressed || statuses[0].Failures != 2 {
		t.Fatalf("unexpected suppression status: %+v", statuses[0])
	}
}

func TestManagerExecNormalizesResponse(t *testing.T) {
	mgr := NewManager()
	sess := newFakeSession(&officialmcp.Tool{Name: "hello", Description: "Hello"})
	sess.callResult = &officialmcp.CallToolResult{
		Content:           []officialmcp.Content{&officialmcp.TextContent{Text: "hi"}},
		StructuredContent: map[string]any{"value": "ok"},
	}
	mgr.SetDial(func(context.Context, ServerConfig, sandbox.Host) (Session, error) { return sess, nil })
	mgr.SetHost(&stdioHostStub{})
	mgr.Reconcile(context.Background(), Config{Servers: []ServerConfig{{Name: "demo", Enabled: true, Transport: TransportStdio, Command: "cmd"}}}, true)
	waitFor(t, time.Second, func() bool { return len(mgr.ValidTools()) == 1 })
	tool := mgr.ValidTools()[0]
	result, err := mgr.Exec(context.Background(), tool.ID, map[string]any{"name": "v"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !result.OK || result.ToolName != "hello" {
		t.Fatalf("unexpected result: %+v", result)
	}
	items, ok := result.Content.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected content: %#v", result.Content)
	}
	if result.Structured["value"] != "ok" {
		t.Fatalf("unexpected structured content: %+v", result.Structured)
	}
}

func TestManagerHandlesNilWaitErrorAsCleanStop(t *testing.T) {
	mgr := NewManager()
	sess := newFakeSession(&officialmcp.Tool{Name: "hello", Description: "Hello"})
	mgr.SetDial(func(context.Context, ServerConfig, sandbox.Host) (Session, error) { return sess, nil })
	mgr.SetHost(&stdioHostStub{})
	mgr.Reconcile(context.Background(), Config{Servers: []ServerConfig{{Name: "demo", Enabled: true, Transport: TransportStdio, Command: "cmd"}}}, true)
	waitFor(t, time.Second, func() bool { return len(mgr.ValidTools()) == 1 })

	sess.waitCh <- nil

	waitFor(t, time.Second, func() bool {
		statuses := mgr.Statuses()
		return len(statuses) == 1 && statuses[0].State == serverStateStopped
	})
	if len(mgr.ValidTools()) != 0 {
		t.Fatalf("expected tools cleared after clean stop, got %+v", mgr.ValidTools())
	}
}
