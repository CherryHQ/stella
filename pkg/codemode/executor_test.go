package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/quickjs"
)

func mustExecutor(t *testing.T, host Host, limits Limits) *Executor {
	t.Helper()
	executor, err := NewExecutor(host, limits)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func jsonHost(value string) HostFunc {
	return func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		return json.RawMessage(value), nil
	}
}

func TestPinnedQuickJSRevision(t *testing.T) {
	if got, want := quickjs.Version(), "2026-06-04"; got != want {
		t.Fatalf("QuickJS revision = %q, want %q", got, want)
	}
}

func TestExecutorPromiseChain(t *testing.T) {
	var got Invocation
	executor := mustExecutor(t, HostFunc(func(_ context.Context, invocation Invocation) (json.RawMessage, error) {
		got = invocation
		return json.RawMessage(`"stella"`), nil
	}), Limits{})

	result, err := executor.Run(context.Background(), `
const name = await tools.invoke("echo", { "kind": "test" });
return { message: name + "!" };
`)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.JSON) != `{"message":"stella!"}` {
		t.Fatalf("result = %s", result.JSON)
	}
	if got.ID != 1 || got.Name != "echo" || string(got.Arguments) != `{"kind":"test"}` {
		t.Fatalf("invocation = %+v", got)
	}
}

func TestExecutorDoesNotInstallAmbientCapabilities(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{})
	result, err := executor.Run(context.Background(), `
return [typeof process, typeof require, typeof fetch, typeof Deno, typeof WASI, typeof std, typeof os, typeof module];
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON), `["undefined","undefined","undefined","undefined","undefined","undefined","undefined","undefined"]`; got != want {
		t.Fatalf("ambient globals = %s, want %s", got, want)
	}

	_, err = executor.Run(context.Background(), `await import("file:///tmp/not-installed.js"); return 1;`)
	if err == nil {
		t.Fatal("module import unexpectedly succeeded")
	}
}

func TestExecutorLimitsAndSerialization(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{SourceBytes: 32, ResultBytes: 8})
	if _, err := executor.Run(context.Background(), strings.Repeat("x", 33)); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("source error = %v, want ErrSourceTooLarge", err)
	}
	if _, err := executor.Run(context.Background(), `return "123456789";`); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("result error = %v, want ErrResultTooLarge", err)
	}

	executor = mustExecutor(t, jsonHost(`null`), Limits{})
	for name, source := range map[string]string{
		"bigint": `return 1n;`,
		"cycle":  `const value = {}; value.self = value; return value;`,
		"error":  `throw new Error("stack marker");`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := executor.Run(context.Background(), source)
			if err == nil {
				t.Fatal("expected serialization or JavaScript error")
			}
			if name == "error" && !strings.Contains(err.Error(), "stack marker") {
				t.Fatalf("error stack missing marker: %v", err)
			}
		})
	}

	result, err := executor.Run(context.Background(), `return new Uint8Array([1, 2, 3]);`)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.JSON) != `{"0":1,"1":2,"2":3}` {
		t.Fatalf("typed array result = %s", result.JSON)
	}
}

func TestExecutorTimeoutInterruptsInfiniteLoop(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{WallClock: 80 * time.Millisecond})
	started := time.Now()
	_, err := executor.Run(context.Background(), `for (;;) {}`)
	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Run error = %v, want ErrTimedOut", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("infinite loop interruption took %s", elapsed)
	}
}

func TestExecutorCancelBeforeActiveIsNotLost(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		executor := mustExecutor(t, jsonHost(`null`), Limits{WallClock: time.Second})
		_, err := executor.Run(ctx, `for (;;) {}`)
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("pre-cancelled Run error = %v, want ErrCancelled", err)
		}
	}
}

func TestExecutorCancelAfterActiveInterruptsOnceAndJoinsWatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	active := make(chan struct{})
	joined := make(chan struct{})
	var (
		mu         sync.Mutex
		states     []executionState
		interrupts atomic.Int32
	)
	executor := mustExecutor(t, jsonHost(`null`), Limits{WallClock: time.Second})
	executor.hooks = &runHooks{
		onInterrupt: func() { interrupts.Add(1) },
		onState: func(state executionState) {
			mu.Lock()
			states = append(states, state)
			mu.Unlock()
		},
		afterWatcherJoined: func() { close(joined) },
	}

	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, `for (;;) {}`)
		done <- err
	}()

	for {
		mu.Lock()
		found := false
		for _, state := range states {
			if state == stateRunning {
				found = true
			}
		}
		mu.Unlock()
		if found {
			close(active)
			break
		}
		time.Sleep(time.Millisecond)
	}
	<-active
	cancel()
	cancel() // Duplicate cancellation must not schedule a second external interrupt.
	if err := <-done; !errors.Is(err, ErrCancelled) {
		t.Fatalf("Run error = %v, want ErrCancelled", err)
	}
	if got := interrupts.Load(); got != 1 {
		t.Fatalf("external interrupts = %d, want 1", got)
	}
	select {
	case <-joined:
	default:
		t.Fatal("owner closed before cancellation watcher joined")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(states) < 4 || states[0] != stateIdle || states[len(states)-1] != stateClosed {
		t.Fatalf("state lifecycle = %v", states)
	}
	for i, state := range states {
		if state == stateClosed && i != len(states)-1 {
			t.Fatalf("Closed appeared before lifecycle end: %v", states)
		}
	}
}

func TestExecutorCancelsBlockingChildAndDropsLateCompletion(t *testing.T) {
	started := make(chan struct{})
	returned := make(chan struct{})
	executor := mustExecutor(t, HostFunc(func(ctx context.Context, _ Invocation) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		close(returned)
		return json.RawMessage(`"late"`), nil
	}), Limits{WallClock: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, `return await tools.invoke("blocking");`)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, ErrCancelled) {
		t.Fatalf("Run error = %v, want ErrCancelled", err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("blocking child was not joined before Run returned")
	}
}

func TestExecutorRejectsHostErrors(t *testing.T) {
	executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		return nil, errors.New("tool failed")
	}), Limits{})
	result, err := executor.Run(context.Background(), `
try { await tools.invoke("fail"); } catch (error) { return String(error); }
`)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.JSON) != `"tool failed"` {
		t.Fatalf("caught host error = %s", result.JSON)
	}
}

func TestExecutorConcurrentVMs(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`42`), Limits{})
	const runs = 16
	errs := make(chan error, runs)
	for range runs {
		go func() {
			result, err := executor.Run(context.Background(), `return await tools.invoke("value");`)
			if err == nil && string(result.JSON) != `42` {
				err = errors.New("unexpected concurrent result")
			}
			errs <- err
		}()
	}
	for range runs {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
