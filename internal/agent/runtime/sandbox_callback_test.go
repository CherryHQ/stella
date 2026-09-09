package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type sandboxRunner struct {
	sess   sandbox.Session
	closed bool
}

type callbackBlockingSandboxRunner struct {
	*sandboxRunner
	closeStarted chan struct{}
	closeRelease chan struct{}
	closeOnce    sync.Once
	closeCount   int
	mu           sync.Mutex
}

func (r *callbackBlockingSandboxRunner) Close() error {
	r.mu.Lock()
	r.closeCount++
	r.mu.Unlock()
	r.closeOnce.Do(func() { close(r.closeStarted) })
	<-r.closeRelease
	r.closed = true
	return r.sess.Close()
}

func (r *sandboxRunner) Chat(context.Context, []ai.Message, MessageContent) <-chan Event {
	ch := make(chan Event)
	close(ch)
	return ch
}
func (r *sandboxRunner) Alive() bool                  { return r.sess.Alive() }
func (r *sandboxRunner) Busy() bool                   { return false }
func (r *sandboxRunner) LastActivity() time.Time      { return time.Now() }
func (r *sandboxRunner) SystemPrompt() string         { return "" }
func (r *sandboxRunner) PluginContext() PluginContext { return PluginContext{} }
func (r *sandboxRunner) SandboxSession() sandbox.Session {
	return r.sess
}

func (r *sandboxRunner) Close() error {
	r.closed = true
	return r.sess.Close()
}

func TestCloseSessionWithSandboxInvokesCallbackBeforeClose(t *testing.T) {
	r := &sandboxRunner{sess: sandbox.NopSession()}
	cache := newRunnerCache(func(context.Context, RunnerParams) (Runner, error) { return r, nil }, fakeMemory{}, 10*time.Minute, slog.Default())
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	called := false
	if err := cache.closeWithSandbox("s1", func(sess sandbox.Session) error {
		called = true
		if sess == nil || !sess.Alive() {
			t.Fatalf("callback got closed sandbox")
		}
		if r.closed {
			t.Fatalf("runner closed before sandbox callback")
		}
		return nil
	}); err != nil {
		t.Fatalf("closeWithSandbox: %v", err)
	}
	if !called {
		t.Fatal("sandbox callback was not called")
	}
	if !r.closed || r.sess.Alive() {
		t.Fatalf("runner/sandbox not closed after callback")
	}
}

func TestCloseSessionWithSandboxClosesAfterCallbackError(t *testing.T) {
	want := errors.New("check failed")
	r := &sandboxRunner{sess: sandbox.NopSession()}
	cache := newRunnerCache(func(context.Context, RunnerParams) (Runner, error) { return r, nil }, fakeMemory{}, 10*time.Minute, slog.Default())
	info := session.NewInfo("s1", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	err := cache.closeWithSandbox("s1", func(sandbox.Session) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("closeWithSandbox err=%v want callback error", err)
	}
	if !r.closed || r.sess.Alive() {
		t.Fatalf("runner/sandbox not closed after callback error")
	}
}

func TestCloseSessionWithSandboxCallbackOwnsRunnerUntilItReturns(t *testing.T) {
	r := &callbackBlockingSandboxRunner{
		sandboxRunner: &sandboxRunner{sess: sandbox.NopSession()},
		closeStarted:  make(chan struct{}),
		closeRelease:  make(chan struct{}),
	}
	cache := newRunnerCache(func(context.Context, RunnerParams) (Runner, error) { return r, nil }, fakeMemory{}, 10*time.Minute, slog.Default())
	info := session.NewInfo("callback-owner", "agent1", "u1", "web", session.KindTask, "", time.Now().UTC())
	if _, _, err := cache.getOrCreate(context.Background(), info, "", ""); err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	callbackEntered := make(chan struct{})
	callbackRelease := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- cache.closeWithSandbox(info.ID, func(sess sandbox.Session) error {
			if sess == nil || !sess.Alive() {
				t.Error("callback got closed sandbox")
			}
			close(callbackEntered)
			<-callbackRelease
			return nil
		})
	}()
	select {
	case <-callbackEntered:
	case <-time.After(time.Second):
		t.Fatal("sandbox callback did not start")
	}
	if err := cache.closeRetiredBatch(); err != nil {
		t.Fatalf("concurrent retired close: %v", err)
	}
	r.mu.Lock()
	closeCount := r.closeCount
	r.mu.Unlock()
	if closeCount != 0 {
		t.Fatalf("concurrent close stole callback owner, close count=%d", closeCount)
	}
	close(callbackRelease)
	close(r.closeRelease)
	if err := <-closeDone; err != nil {
		t.Fatalf("closeWithSandbox: %v", err)
	}
	select {
	case <-r.closeStarted:
	default:
		t.Fatal("runner close did not run after callback")
	}
	r.mu.Lock()
	closeCount = r.closeCount
	r.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("runner close count=%d, want 1", closeCount)
	}
}
