package sandbox

import (
	"context"
	"errors"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
)

// mockSession is a controllable Session for testing ResilientSession.
type mockSession struct {
	mu             sync.Mutex
	alive          bool
	closed         bool
	execCount      int
	lastExecEnv    map[string]string
	lastProcessEnv map[string]string
	done           chan struct{}
}

func newMockSession() *mockSession {
	return &mockSession{alive: true, done: make(chan struct{})}
}

func (m *mockSession) Policy() Policy        { return Policy{} }
func (m *mockSession) WorkingDir() string    { return "/workspace" }
func (m *mockSession) Done() <-chan struct{} { return m.done }

func (m *mockSession) Alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive
}

func (m *mockSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alive = false
	m.closed = true
	return nil
}

func (m *mockSession) Exec(_ context.Context, _ string, opts ExecOptions) (ExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCount++
	m.lastExecEnv = maps.Clone(opts.Env)
	return ExecResult{Stdout: "ok"}, nil
}

func (m *mockSession) StartProcess(_ context.Context, req ProcessRequest) (ProcessHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastProcessEnv = maps.Clone(req.Env)
	return nil, nil
}

func (m *mockSession) ResolvePath(path string) (string, error)      { return path, nil }
func (m *mockSession) ResolveWritePath(path string) (string, error) { return path, nil }

func TestResilientSession_ExecUsesExistingSession(t *testing.T) {
	s := newMockSession()
	rs := NewResilientSession(s, func(_ context.Context) (Session, error) {
		t.Fatal("should not recreate")
		return nil, nil
	})

	result, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}
	if result.Stdout != "ok" {
		t.Errorf("got %q, want %q", result.Stdout, "ok")
	}
}

func TestResilientSessionRefreshEnvOverlaysSubsequentProcesses(t *testing.T) {
	inner := newMockSession()
	rs := NewResilientSession(inner, nil)
	rs.RefreshEnv(map[string]string{"TOKEN": "new", "CALLER": "overlay"})

	if _, err := rs.Exec(context.Background(), "true", ExecOptions{Env: map[string]string{"CALLER": "exec"}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := inner.lastExecEnv["TOKEN"]; got != "new" {
		t.Fatalf("Exec TOKEN = %q, want new", got)
	}
	if got := inner.lastExecEnv["CALLER"]; got != "exec" {
		t.Fatalf("per-exec env must win; CALLER = %q", got)
	}

	if _, err := rs.StartProcess(context.Background(), ProcessRequest{}); err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	if got := inner.lastProcessEnv["TOKEN"]; got != "new" {
		t.Fatalf("StartProcess TOKEN = %q, want new", got)
	}
	if got := rs.Policy().Env["TOKEN"]; got != "new" {
		t.Fatalf("Policy TOKEN = %q, want new", got)
	}
}

func TestResilientSession_RecreatesAfterClose(t *testing.T) {
	first := newMockSession()
	second := newMockSession()

	var createCount atomic.Int32
	rs := NewResilientSession(first, func(_ context.Context) (Session, error) {
		createCount.Add(1)
		return second, nil
	})
	rs.RefreshEnv(map[string]string{"TOKEN": "old-overlay"})

	// Close the underlying session (simulates reaper). The recreated session is
	// built from current state, so the dead session's overlay must be discarded.
	_ = first.Close()

	result, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}
	if result.Stdout != "ok" {
		t.Errorf("got %q, want %q", result.Stdout, "ok")
	}
	if createCount.Load() != 1 {
		t.Errorf("create called %d times, want 1", createCount.Load())
	}
	if _, ok := second.lastExecEnv["TOKEN"]; ok {
		t.Fatal("recreated session inherited a stale env overlay")
	}

	// Second exec should reuse the recreated session.
	_, err = rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err != nil {
		t.Fatalf("second Exec error: %v", err)
	}
	if createCount.Load() != 1 {
		t.Errorf("create called %d times after second exec, want 1", createCount.Load())
	}
}

func TestResilientSession_PermanentCloseRejectsExec(t *testing.T) {
	s := newMockSession()
	rs := NewResilientSession(s, func(_ context.Context) (Session, error) {
		t.Fatal("should not recreate after permanent close")
		return nil, nil
	})

	_ = rs.Close()

	_, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err == nil {
		t.Fatal("Exec should fail after permanent Close")
	}
}

// fsMockSession adds the mediated filesystem capability to mockSession.
type fsMockSession struct {
	*mockSession
	fs  Filesystem
	err error
}

func (m fsMockSession) Filesystem() (Filesystem, error) { return m.fs, m.err }

func TestResilientSession_FilesystemForwardsToInner(t *testing.T) {
	sentinel := errors.New("provider filesystem")
	rs := NewResilientSession(fsMockSession{mockSession: newMockSession(), err: sentinel}, func(_ context.Context) (Session, error) {
		t.Fatal("should not recreate")
		return nil, nil
	})
	if _, err := rs.Filesystem(); !errors.Is(err, sentinel) {
		t.Fatalf("Filesystem() err = %v, want forwarded inner error", err)
	}
}

func TestResilientSession_FilesystemMissingCapability(t *testing.T) {
	rs := NewResilientSession(newMockSession(), func(_ context.Context) (Session, error) {
		t.Fatal("should not recreate")
		return nil, nil
	})
	if _, err := rs.Filesystem(); err == nil {
		t.Fatal("Filesystem() must fail closed when the inner lacks the capability")
	}
}

func TestResilientSession_RecreateFailurePropagates(t *testing.T) {
	first := newMockSession()
	_ = first.Close()

	rs := NewResilientSession(first, func(_ context.Context) (Session, error) {
		return nil, errors.New("docker daemon unreachable")
	})

	_, err := rs.Exec(context.Background(), "echo hi", ExecOptions{})
	if err == nil {
		t.Fatal("Exec should fail when recreate fails")
	}
}

func TestResilientSession_AliveReflectsInner(t *testing.T) {
	s := newMockSession()
	rs := NewResilientSession(s, nil)

	if !rs.Alive() {
		t.Error("should be alive initially")
	}

	_ = s.Close()
	if rs.Alive() {
		t.Error("should not be alive after inner close")
	}
}

func TestResilientSession_ConcurrentExecRecreatesOnce(t *testing.T) {
	first := newMockSession()
	_ = first.Close()

	var createCount atomic.Int32
	rs := NewResilientSession(first, func(_ context.Context) (Session, error) {
		createCount.Add(1)
		return newMockSession(), nil
	})

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, _ = rs.Exec(context.Background(), "echo hi", ExecOptions{})
		})
	}
	wg.Wait()

	if createCount.Load() != 1 {
		t.Errorf("create called %d times, want 1 (should deduplicate)", createCount.Load())
	}
}
