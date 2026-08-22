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

func TestExecutorDefaultLimitsAreFixedPhaseThreeBudget(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{})
	got := executor.limits
	if got.SourceBytes != 100<<10 || got.WallClock != 30*time.Second || got.MemoryBytes != 64<<20 || got.MaxStackSlots != 1024 || got.MaxCalls != 64 || got.LogEntries != 256 || got.LogBytes != 256<<10 || got.PayloadBytes != 1<<20 || got.ResultBytes != 1<<20 {
		t.Fatalf("default limits = %#v", got)
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

func TestExecutorEnforcesPayloadCallAndLogLimits(t *testing.T) {
	t.Run("argument payload", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`null`), Limits{PayloadBytes: 16})
		_, err := executor.Run(context.Background(), `await tools.invoke("echo", { value: "this is too large" }); return "unreachable";`)
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("Run error = %v, want ErrPayloadTooLarge", err)
		}
	})
	t.Run("completion payload", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`"this completion is too large"`), Limits{PayloadBytes: 16})
		_, err := executor.Run(context.Background(), `return await tools.invoke("echo");`)
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("Run error = %v, want ErrPayloadTooLarge", err)
		}
	})
	t.Run("sixty fifth call drains first sixty four", func(t *testing.T) {
		var ids []uint64
		var mu sync.Mutex
		executor := mustExecutor(t, HostFunc(func(_ context.Context, invocation Invocation) (json.RawMessage, error) {
			mu.Lock()
			ids = append(ids, invocation.ID)
			mu.Unlock()
			return json.RawMessage(`null`), nil
		}), Limits{MaxCalls: 64})
		_, err := executor.Run(context.Background(), `
const calls = [];
for (let i = 0; i < 65; i++) calls.push(tools.invoke("effect" + i));
await Promise.all(calls);
return "unreachable";
`)
		if !errors.Is(err, ErrInvocationLimit) {
			t.Fatalf("Run error = %v, want ErrInvocationLimit", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(ids) != 64 {
			t.Fatalf("completed child calls = %d, want 64", len(ids))
		}
		for i, id := range ids {
			if want := uint64(i + 1); id != want {
				t.Fatalf("child ID %d = %d, want %d", i, id, want)
			}
		}
	})
	t.Run("log entries", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`null`), Limits{LogEntries: 2})
		_, err := executor.Run(context.Background(), `console.log("one"); console.info("two"); console.error("three"); return "unreachable";`)
		if !errors.Is(err, ErrLogTooLarge) {
			t.Fatalf("Run error = %v, want ErrLogTooLarge", err)
		}
	})
	t.Run("log bytes", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`null`), Limits{LogBytes: 8})
		_, err := executor.Run(context.Background(), `console.log("too many bytes"); return "unreachable";`)
		if !errors.Is(err, ErrLogTooLarge) {
			t.Fatalf("Run error = %v, want ErrLogTooLarge", err)
		}
	})
}

func TestExecutorFatalBridgeLimitsCannotBeCaught(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limits Limits
		source string
		want   error
	}{
		{
			name:   "argument payload",
			limits: Limits{PayloadBytes: 16},
			source: `try { await tools.invoke("first", { value: "this is too large" }); } catch (_) {} tools.invoke("after"); return "ok";`,
			want:   ErrPayloadTooLarge,
		},
		{
			name:   "log budget",
			limits: Limits{LogBytes: 8},
			source: `try { console.log("too many bytes"); } catch (_) {} tools.invoke("after"); return "ok";`,
			want:   ErrLogTooLarge,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			executor := mustExecutor(t, HostFunc(func(_ context.Context, invocation Invocation) (json.RawMessage, error) {
				calls = append(calls, invocation.Name)
				return json.RawMessage(`null`), nil
			}), tt.limits)
			_, err := executor.Run(context.Background(), tt.source)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Run error = %v, want %v", err, tt.want)
			}
			if len(calls) != 0 {
				t.Fatalf("fatal bridge limit admitted calls = %v", calls)
			}
		})
	}

	t.Run("invocation error value", func(t *testing.T) {
		var calls []string
		executor := mustExecutor(t, HostFunc(func(_ context.Context, invocation Invocation) (json.RawMessage, error) {
			calls = append(calls, invocation.Name)
			if invocation.Name == "first" {
				return nil, &InvocationError{Value: json.RawMessage(`"this structured value is too large"`), Err: errors.New("business failure")}
			}
			return json.RawMessage(`null`), nil
		}), Limits{PayloadBytes: 16})
		_, err := executor.Run(context.Background(), `try { await tools.invoke("first"); } catch (_) {} tools.invoke("after"); return "ok";`)
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("Run error = %v, want ErrPayloadTooLarge", err)
		}
		if got, want := strings.Join(calls, ","), "first"; got != want {
			t.Fatalf("fatal structured value admitted calls = %q, want %q", got, want)
		}
	})
}

func TestExecutorCaughtGuestResourceErrorsRemainLanguageErrors(t *testing.T) {
	for _, source := range []string{
		`try { function recur() { return recur(); } recur(); } catch (_) {} return await tools.invoke("effect");`,
		`try { new ArrayBuffer(128 << 20); } catch (_) {} return await tools.invoke("effect");`,
	} {
		t.Run(source[:20], func(t *testing.T) {
			called := false
			executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
				called = true
				return json.RawMessage(`"ok"`), nil
			}), Limits{MemoryBytes: 1 << 20, MaxStackSlots: 256})
			result, err := executor.Run(context.Background(), source)
			if err != nil || !called || string(result.JSON) != `"ok"` {
				t.Fatalf("caught guest resource result=%s err=%v called=%v", result.JSON, err, called)
			}
		})
	}
}

func TestExecutorGlobalVMAdmissionIsContextAware(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	runs := make([]chan error, 4)
	for i := range runs {
		executor := mustExecutor(t, jsonHost(`null`), Limits{})
		executor.hooks = &runHooks{beforeActivate: func() { entered <- struct{}{}; <-release }}
		runs[i] = make(chan error, 1)
		go func(done chan<- error) { _, err := executor.Run(context.Background(), `return null;`); done <- err }(runs[i])
	}
	for range runs {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("active VM did not start")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	fifthEntered := make(chan struct{}, 1)
	fifth := mustExecutor(t, jsonHost(`null`), Limits{})
	fifth.hooks = &runHooks{beforeActivate: func() { fifthEntered <- struct{}{} }}
	fifthDone := make(chan error, 1)
	go func() { _, err := fifth.Run(ctx, `return null;`); fifthDone <- err }()
	cancel()
	select {
	case err := <-fifthDone:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled waiter error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled VM waiter did not return")
	}
	select {
	case <-fifthEntered:
		t.Fatal("cancelled waiter created a VM")
	default:
	}
	close(release)
	for _, done := range runs {
		if err := <-done; err != nil {
			t.Fatalf("held VM error = %v", err)
		}
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

func TestExecutorDrainsUnawaitedChildBeforeOuterSuccess(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`"side effect"`), nil
	}), Limits{WallClock: time.Second})
	done := make(chan runResult, 1)
	go func() {
		result, err := executor.Run(context.Background(), `Promise.resolve().then(() => tools.invoke("effect")); return "ok";`)
		done <- runResult{result: result, err: err}
	}()
	<-started
	select {
	case outcome := <-done:
		t.Fatalf("outer returned before unawaited child: result=%s err=%v", outcome.result.JSON, outcome.err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	outcome := <-done
	if outcome.err != nil || string(outcome.result.JSON) != `"ok"` {
		t.Fatalf("unawaited success = result:%s err:%v", outcome.result.JSON, outcome.err)
	}
}

func TestExecutorFailsOnUnawaitedChildError(t *testing.T) {
	executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		return nil, &InvocationError{Value: json.RawMessage(`{"isError":true}`), Err: errors.New("side effect failed")}
	}), Limits{})
	_, err := executor.Run(context.Background(), `tools.invoke("effect"); return "ok";`)
	if err == nil || !strings.Contains(err.Error(), "side effect failed") || strings.Contains(err.Error(), "[object Object]") {
		t.Fatalf("unawaited child error = %v", err)
	}
}

func TestExecutorTreatsChainedCatchAsObserved(t *testing.T) {
	executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		return nil, &InvocationError{Value: json.RawMessage(`{"isError":true}`), Err: errors.New("handled failure")}
	}), Limits{})
	result, err := executor.Run(context.Background(), `
tools.invoke("bad").then(() => "unexpected").catch(error => error.message);
return "ok";
`)
	if err != nil || string(result.JSON) != `"ok"` {
		t.Fatalf("chained catch = result:%s err:%v", result.JSON, err)
	}
}

func TestExecutorInvokeReturnsNativePromise(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{})
	result, err := executor.Run(context.Background(), `
const promise = tools.invoke("ok");
return { instance: promise instanceof Promise, tag: Object.prototype.toString.call(promise) };
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON), `{"instance":true,"tag":"[object Promise]"}`; got != want {
		t.Fatalf("tools.invoke promise = %s, want %s", got, want)
	}
}

func TestExecutorCancelsUnawaitedChildAndJoinsWorker(t *testing.T) {
	started := make(chan struct{})
	returned := make(chan struct{})
	executor := mustExecutor(t, HostFunc(func(ctx context.Context, _ Invocation) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		close(returned)
		return nil, ctx.Err()
	}), Limits{WallClock: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, `tools.invoke("effect"); return "ok";`)
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
		t.Fatal("unawaited child worker survived cancellation")
	}
}

func TestExecutorRejectsStructuredInvocationErrorAsStableJSError(t *testing.T) {
	executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		return nil, &InvocationError{Value: json.RawMessage(`{"blocks":[{"type":"text","text":"normalized"}],"isError":true}`), Err: errors.New("tool failed")}
	}), Limits{})
	result, err := executor.Run(context.Background(), `
try {
  await tools.invoke("fail");
} catch (error) {
  return { isError: error instanceof Error, message: error.message, name: error.name, code: error.code, value: error.value };
}
`)
	if err != nil {
		t.Fatal(err)
	}
	var caught struct {
		IsError bool
		Message string
		Name    string
		Code    string
		Value   struct {
			IsError bool
			Blocks  []struct{ Text string }
		}
	}
	if err := json.Unmarshal(result.JSON, &caught); err != nil {
		t.Fatal(err)
	}
	if !caught.IsError || caught.Message != "tool failed" || caught.Name != "ToolInvocationError" || caught.Code != invocationErrorCode || !caught.Value.IsError || len(caught.Value.Blocks) != 1 || caught.Value.Blocks[0].Text != "normalized" {
		t.Fatalf("caught host error = %s", result.JSON)
	}
	_, err = executor.Run(context.Background(), `await tools.invoke("fail");`)
	if err == nil || !strings.Contains(err.Error(), "tool failed") || strings.Contains(err.Error(), "[object Object]") {
		t.Fatalf("uncaught host error = %v, want stable child failure", err)
	}
}

func TestExecutorDoesNotExposeInfrastructureErrorsToJavaScript(t *testing.T) {
	executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		return nil, errors.New("canonicalizer failed")
	}), Limits{})
	_, err := executor.Run(context.Background(), `
try { await tools.invoke("fail"); return "caught"; } catch (_) { return "swallowed"; }
`)
	if err == nil || !strings.Contains(err.Error(), "canonicalizer failed") {
		t.Fatalf("infrastructure error = %v, want outer failure", err)
	}
}

type blockingCatalogHost struct{ searchCalled atomic.Bool }

func (h *blockingCatalogHost) Invoke(context.Context, Invocation) (json.RawMessage, error) {
	return json.RawMessage(`null`), nil
}

// These methods intentionally resemble the removed callback SPI. The executor
// must ignore them and use only its copied CatalogEntry data.
func (h *blockingCatalogHost) Search(string) (any, error) {
	h.searchCalled.Store(true)
	select {}
}

func (*blockingCatalogHost) Describe(string) (any, error) { select {} }

func TestExecutorCatalogIsCopiedPureData(t *testing.T) {
	host := &blockingCatalogHost{}
	entries := []CatalogEntry{{Name: "visible", Description: "pure", InputSchema: map[string]any{"type": "object"}}}
	executor, err := NewExecutor(host, Limits{}, entries...)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Name = "mutated"
	entries[0].InputSchema["type"] = "mutated"
	result, err := executor.Run(context.Background(), `return { search: tools.search(""), describe: tools.describe("visible") };`)
	if err != nil {
		t.Fatal(err)
	}
	if host.searchCalled.Load() {
		t.Fatal("executor called a host catalog callback")
	}
	var got struct {
		Search   []struct{ Name string }
		Describe struct {
			Name        string
			Description string
			InputSchema map[string]any
		}
	}
	if err := json.Unmarshal(result.JSON, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Search) != 1 || got.Search[0].Name != "visible" || got.Describe.Name != "visible" || got.Describe.Description != "pure" || got.Describe.InputSchema["type"] != "object" {
		t.Fatalf("catalog = %s", result.JSON)
	}
}

func TestExecutorCancellationSurvivesCompletionRaceBeforeInfiniteContinuation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	executor := mustExecutor(t, HostFunc(func(ctx context.Context, _ Invocation) (json.RawMessage, error) {
		close(started)
		select {
		case <-release:
			return json.RawMessage(`1`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}), Limits{WallClock: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, `await tools.invoke("race"); for (;;) {}`)
		done <- err
	}()
	<-started
	// The completion is made eligible first. The owner must still see the
	// persistent cancellation state before settling or running its continuation.
	close(release)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("Run error = %v, want ErrCancelled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation was lost to an infinite continuation")
	}
}

func TestExecutorRunsChildInvocationsFIFO(t *testing.T) {
	var (
		mu        sync.Mutex
		order     []string
		active    int
		maxActive int
	)
	executor := mustExecutor(t, HostFunc(func(_ context.Context, invocation Invocation) (json.RawMessage, error) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		order = append(order, invocation.Name)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return json.RawMessage(`null`), nil
	}), Limits{})

	_, err := executor.Run(context.Background(), `
await Promise.all([tools.invoke("one"), tools.invoke("two"), tools.invoke("three")]);
return "done";
`)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("maximum host concurrency = %d, want 1", maxActive)
	}
	if got, want := strings.Join(order, ","), "one,two,three"; got != want {
		t.Fatalf("host order = %s, want %s", got, want)
	}
}

func TestExecutorCompletionStateIsNotScriptMutable(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{})
	result, err := executor.Run(context.Background(), `
Object.defineProperty(globalThis, "__stellaComplete", { value: () => { throw new Error("tampered"); } });
Object.defineProperty(globalThis, "__stellaDone", { value: false, writable: true });
Object.defineProperty(globalThis, "__stellaResult", { value: "not json", writable: true });
return { safe: true };
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON), `{"safe":true}`; got != want {
		t.Fatalf("result = %s, want %s", got, want)
	}
	if !json.Valid(result.JSON) {
		t.Fatalf("result is not valid JSON: %q", result.JSON)
	}
}

func TestExecutorUserSourceCannotForgeRootCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`"actual"`), nil
	}), Limits{WallClock: time.Second})

	done := make(chan runResult, 1)
	go func() {
		result, err := executor.Run(context.Background(), `
try { complete("forged"); } catch (_) {}
const value = await tools.invoke("blocking");
return { value };
`)
		done <- runResult{result: result, err: err}
	}()
	<-started
	select {
	case outcome := <-done:
		t.Fatalf("source forged root completion: result=%s err=%v", outcome.result.JSON, outcome.err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	outcome := <-done
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if got, want := string(outcome.result.JSON), `{"value":"actual"}`; got != want {
		t.Fatalf("result = %s, want %s", got, want)
	}
}

func TestExecutorUsesOneAbsoluteWallClockBudgetAcrossJobs(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{WallClock: 35 * time.Millisecond})
	started := time.Now()
	_, err := executor.Run(context.Background(), `
const busy = ms => { const until = Date.now() + ms; while (Date.now() < until) {} };
await tools.invoke("one"); busy(20);
await tools.invoke("two"); busy(20);
await tools.invoke("three"); busy(20);
return "unreachable";
`)
	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Run error = %v, want ErrTimedOut", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("wall-clock budget was renewed across jobs: %s", elapsed)
	}
}

func TestExecutorUserInterruptedErrorIsNotTimeout(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{WallClock: time.Second})
	_, err := executor.Run(context.Background(), `throw new Error("interrupted");`)
	if err == nil || errors.Is(err, ErrTimedOut) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("Run error = %v, want ordinary user error containing interrupted", err)
	}
}

func TestExecutorUserInternalErrorIsNotTimeout(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{WallClock: time.Second})
	_, err := executor.Run(context.Background(), `throw new InternalError("user error");`)
	if err == nil || errors.Is(err, ErrTimedOut) || !strings.Contains(err.Error(), "user error") {
		t.Fatalf("Run error = %v, want ordinary user InternalError", err)
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
