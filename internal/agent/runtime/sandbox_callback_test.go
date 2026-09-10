package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type sandboxRunner struct {
	sess   sandbox.Session
	closed bool
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

func TestSandboxResultRunsBeforeCompletionWithExecutionContext(t *testing.T) {
	runner := &sandboxRunner{sess: sandbox.NopSession()}
	mem := &activityRecordingMemory{}
	rt, err := New(Config{LocalOnly: true, Memory: mem, NewRunner: func(context.Context, RunnerParams) (Runner, error) { return runner, nil }})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	info := session.NewInfo("sandbox-result", "a1", "u1", "task", session.KindTask, "", time.Now().UTC())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := rt.ChatAdmitted(ctx, info, "hello", WithSandboxResult(func(runCtx context.Context, sess sandbox.Session) error {
		if memory.SessionIDFromContext(runCtx) != info.ID {
			t.Error("callback lost execution context")
		}
		if !sess.Alive() || runner.closed {
			t.Error("sandbox closed before callback")
		}
		close(entered)
		select {
		case <-release:
		case <-runCtx.Done():
			return runCtx.Err()
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	if _, err := rt.ChatAdmitted(t.Context(), info, "second"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("concurrent turn: %v", err)
	}
	if got := mem.activitySnapshot(); len(got) != 1 {
		t.Fatalf("premature completion: %v", got)
	}
	cancel()
	for range stream {
	}
	if err := rt.CloseSession(t.Context(), info.ID); err != nil {
		t.Fatal(err)
	}
	if !runner.closed {
		t.Fatal("caller failed to close runner")
	}
}

func TestSandboxResultFailureFailsRun(t *testing.T) {
	boom := errors.New("check failed")
	runner := &sandboxRunner{sess: sandbox.NopSession()}
	mem := &activityRecordingMemory{}
	rt, err := New(Config{LocalOnly: true, Memory: mem, NewRunner: func(context.Context, RunnerParams) (Runner, error) { return runner, nil }})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("sandbox-error", "a1", "u1", "task", session.KindTask, "", time.Now().UTC())
	var got error
	for event := range rt.Chat(t.Context(), info, "hello", WithSandboxResult(func(context.Context, sandbox.Session) error { return boom })) {
		if event.Err != nil {
			got = event.Err
		}
	}
	if !errors.Is(got, boom) {
		t.Fatalf("error = %v", got)
	}
	if states := mem.activitySnapshot(); len(states) != 2 || states[1] != "completed:error" {
		t.Fatalf("activity = %v", states)
	}
	if err := rt.CloseSession(t.Context(), info.ID); err != nil {
		t.Fatal(err)
	}
}

// The terminal predicate stops model work without cancelling result checks.
type terminalSandboxRunner struct {
	*sandboxRunner
	stopped chan struct{}
}

func (r *terminalSandboxRunner) Chat(ctx context.Context, _ []ai.Message, _ MessageContent) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		out <- Event{Text: "submitted"}
		<-ctx.Done()
		close(r.stopped)
	}()
	return out
}

func TestTerminalResultStopsOnlyModelBeforeSandboxCheck(t *testing.T) {
	runner := &terminalSandboxRunner{sandboxRunner: &sandboxRunner{sess: sandbox.NopSession()}, stopped: make(chan struct{})}
	mem := &activityRecordingMemory{}
	rt, err := New(Config{LocalOnly: true, Memory: mem, NewRunner: func(context.Context, RunnerParams) (Runner, error) { return runner, nil }})
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("terminal-result", "a1", "u1", "task", session.KindTask, "", time.Now().UTC())
	checked := false
	for event := range rt.Chat(t.Context(), info, "hello", WithOneShotRunner(), WithStopWhen(func() bool { return true }), WithSandboxResult(func(ctx context.Context, _ sandbox.Session) error {
		if err := ctx.Err(); err != nil {
			t.Errorf("result context canceled: %v", err)
		}
		select {
		case <-runner.stopped:
		default:
			t.Error("model still producing during result check")
		}
		checked = true
		return nil
	})) {
		if event.Err != nil {
			t.Errorf("terminal run: %v", event.Err)
		}
	}
	if !checked {
		t.Fatal("terminal submit skipped checks")
	}
	if !runner.closed {
		t.Fatal("one-shot runner survived EOF")
	}
	if got := mem.activitySnapshot(); len(got) != 2 || got[1] != "completed:success" {
		t.Fatalf("activity=%v", got)
	}
}
