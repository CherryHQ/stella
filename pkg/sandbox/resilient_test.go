package sandbox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// mockSession is a controllable Session for testing ResilientSession.
type mockSession struct {
	mu        sync.Mutex
	alive     bool
	closed    bool
	execCount int
	done      chan struct{}
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

func (m *mockSession) Exec(_ context.Context, _ string, _ ExecOptions) (ExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCount++
	return ExecResult{Stdout: "ok"}, nil
}

func (m *mockSession) StartProcess(_ context.Context, _ ProcessRequest) (ProcessHandle, error) {
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

func TestResilientSession_RecreatesAfterClose(t *testing.T) {
	first := newMockSession()
	second := newMockSession()

	var createCount atomic.Int32
	rs := NewResilientSession(first, func(_ context.Context) (Session, error) {
		createCount.Add(1)
		return second, nil
	})

	// Close the underlying session (simulates reaper).
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
