package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestExecutorToolResultHelpers(t *testing.T) {
	executor := mustExecutor(t, HostFunc(func(_ context.Context, invocation Invocation) (json.RawMessage, error) {
		value := json.RawMessage(`{"kind":"stella.tool_value","version":1,"blocks":[{"type":"text","text":"{\"answer\":42}"}],"isError":false}`)
		if invocation.Name == "fail" {
			return nil, &InvocationError{Value: value, Err: errors.New("tool invocation failed")}
		}
		return value, nil
	}), Limits{})

	result, err := executor.Run(context.Background(), `
const success = await tools.invoke("ok");
let failure;
try {
  await tools.invoke("fail");
} catch (error) {
  failure = tools.json(error.value);
}
return { text: tools.text(success), parsed: tools.json(success), failure };
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON), `{"text":"{\"answer\":42}","parsed":{"answer":42},"failure":{"answer":42}}`; got != want {
		t.Fatalf("tool helpers = %s, want %s", got, want)
	}
}

func TestExecutorCopiesLargeSerializedValues(t *testing.T) {
	const payloadBytes = 256 << 10
	executor := mustExecutor(t, HostFunc(func(_ context.Context, invocation Invocation) (json.RawMessage, error) {
		var arguments struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
			return nil, err
		}
		if len(arguments.Payload) != payloadBytes {
			return nil, fmt.Errorf("payload length = %d, want %d", len(arguments.Payload), payloadBytes)
		}
		return json.RawMessage(`null`), nil
	}), Limits{})

	result, err := executor.Run(context.Background(), `
const payload = "x".repeat(256 << 10);
await tools.invoke("echo", { payload });
return payload;
`)
	if err != nil {
		t.Fatal(err)
	}
	var resultPayload string
	if err := json.Unmarshal(result.JSON, &resultPayload); err != nil {
		t.Fatal(err)
	}
	if len(resultPayload) != payloadBytes {
		t.Fatalf("result length = %d, want %d", len(resultPayload), payloadBytes)
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
		"bigint":        `return 1n;`,
		"nested bigint": `return { oauth: { count: 1n } };`,
		"cycle":         `const value = {}; value.self = value; return value;`,
		"map":           `return { values: new Map([["a", 1]]) };`,
		"non-finite":    `return { score: NaN };`,
		"error":         `throw new Error("stack marker");`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := executor.Run(context.Background(), source)
			if err == nil {
				t.Fatal("expected serialization or JavaScript error")
			}
			if name == "error" && !strings.Contains(err.Error(), "stack marker") {
				t.Fatalf("error stack missing marker: %v", err)
			}
			if name == "nested bigint" && !strings.Contains(err.Error(), "return.oauth.count is a bigint") {
				t.Fatalf("nested serialization path missing: %v", err)
			}
			if strings.Contains(err.Error(), "infrastructure failure") {
				t.Fatalf("guest serialization error classified as infrastructure: %v", err)
			}
		})
	}

	result, err := executor.Run(context.Background(), `return { kept: 1, omitted: undefined, array: [1, undefined] };`)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.JSON) != `{"kept":1,"array":[1,null]}` {
		t.Fatalf("undefined JSON semantics = %s", result.JSON)
	}

	result, err = executor.Run(context.Background(), `return new Uint8Array([1, 2, 3]);`)
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
		if len(ids) > 1 {
			t.Fatalf("owner fatal started queued child calls = %d, want at most the inflight call", len(ids))
		}
	})
	t.Run("log entries beyond the budget are dropped, not fatal", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`null`), Limits{LogEntries: 2})
		result, err := executor.Run(context.Background(), `console.log("one"); console.info("two"); console.error("three"); return "reached";`)
		if err != nil {
			t.Fatalf("Run error = %v, want nil", err)
		}
		if got := string(result.JSON); got != `"reached"` {
			t.Fatalf("Run result = %s, want %q", got, `"reached"`)
		}
	})
	t.Run("log bytes beyond the budget are dropped, not fatal", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`null`), Limits{LogBytes: 8})
		result, err := executor.Run(context.Background(), `console.log("too many bytes"); return "reached";`)
		if err != nil {
			t.Fatalf("Run error = %v, want nil", err)
		}
		if got := string(result.JSON); got != `"reached"` {
			t.Fatalf("Run result = %s, want %q", got, `"reached"`)
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

func TestExecutorWorkerFatalClosesAdmissionBeforeCompletion(t *testing.T) {
	for _, tt := range []struct {
		name string
		host HostFunc
	}{
		{
			name: "normal completion",
			host: func(_ context.Context, _ Invocation) (json.RawMessage, error) {
				return json.RawMessage(`"this normal completion is too large"`), nil
			},
		},
		{
			name: "structured rejection",
			host: func(_ context.Context, _ Invocation) (json.RawMessage, error) {
				return nil, &InvocationError{Value: json.RawMessage(`"this business value is too large"`), Err: errors.New("business failure")}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			latched := make(chan struct{})
			var calls []string
			executor := mustExecutor(t, HostFunc(func(ctx context.Context, invocation Invocation) (json.RawMessage, error) {
				calls = append(calls, invocation.Name)
				return tt.host(ctx, invocation)
			}), Limits{PayloadBytes: 16})
			executor.hooks = &runHooks{
				afterInvokeEnqueued: func() { <-latched },
				onWorkerFatal:       func() { close(latched) },
			}
			_, err := executor.Run(context.Background(), `
tools.invoke("first");
await Promise.resolve();
for (let i = 0; i < 64; i++) tools.invoke("after" + i);
return "unreachable";
`)
			if !errors.Is(err, ErrPayloadTooLarge) {
				t.Fatalf("Run error = %v, want ErrPayloadTooLarge", err)
			}
			if got, want := strings.Join(calls, ","), "first"; got != want {
				t.Fatalf("worker fatal admitted later calls = %q, want %q", got, want)
			}
		})
	}
}

func TestExecutorWorkerFatalStopsAlreadyQueuedChildrenAfterOwnerFatal(t *testing.T) {
	for _, tt := range []struct {
		name        string
		workerReply func() (json.RawMessage, error)
	}{
		{
			name: "oversized normal completion",
			workerReply: func() (json.RawMessage, error) {
				return json.RawMessage(`"this completion is too large"`), nil
			},
		},
		{
			name: "oversized structured rejection",
			workerReply: func() (json.RawMessage, error) {
				return nil, &InvocationError{Value: json.RawMessage(`"this business value is too large"`), Err: errors.New("business failure")}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			firstStarted := make(chan struct{})
			secondQueued := make(chan struct{})
			allowOwnerFatal := make(chan struct{})
			ownerFatal := make(chan struct{})
			releaseFirst := make(chan struct{})
			var firstStartedOnce, secondQueuedOnce, allowOwnerFatalOnce, ownerFatalOnce, releaseFirstOnce sync.Once
			defer func() {
				allowOwnerFatalOnce.Do(func() { close(allowOwnerFatal) })
				releaseFirstOnce.Do(func() { close(releaseFirst) })
			}()

			var queuedSideEffects atomic.Int32
			executor := mustExecutor(t, HostFunc(func(ctx context.Context, invocation Invocation) (json.RawMessage, error) {
				switch invocation.Name {
				case "first":
					firstStartedOnce.Do(func() { close(firstStarted) })
					select {
					case <-releaseFirst:
						return tt.workerReply()
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				case "queued":
					queuedSideEffects.Add(1)
					return json.RawMessage(`null`), nil
				default:
					return nil, fmt.Errorf("unexpected invocation %q", invocation.Name)
				}
			}), Limits{PayloadBytes: 16})
			var enqueued atomic.Int32
			executor.hooks = &runHooks{
				afterInvokeEnqueued: func() {
					if enqueued.Add(1) == 2 {
						secondQueuedOnce.Do(func() { close(secondQueued) })
						<-allowOwnerFatal
					}
				},
				onOwnerFatal: func() { ownerFatalOnce.Do(func() { close(ownerFatal) }) },
			}
			done := make(chan error, 1)
			go func() {
				_, err := executor.Run(ctx, `
tools.invoke("first");
tools.invoke("queued");
tools.invoke("oversized", { value: "this argument is too large" });
return "unreachable";
`)
				done <- err
			}()

			for name, signal := range map[string]<-chan struct{}{"first worker": firstStarted, "queued request": secondQueued} {
				select {
				case <-signal:
				case <-ctx.Done():
					t.Fatalf("%s was not reached: %v", name, ctx.Err())
				}
			}
			allowOwnerFatalOnce.Do(func() { close(allowOwnerFatal) })
			select {
			case <-ownerFatal:
			case <-ctx.Done():
				t.Fatalf("owner payload fatal was not latched: %v", ctx.Err())
			}
			releaseFirstOnce.Do(func() { close(releaseFirst) })

			select {
			case err := <-done:
				if !errors.Is(err, ErrPayloadTooLarge) {
					t.Fatalf("Run error = %v, want owner ErrPayloadTooLarge", err)
				}
			case <-ctx.Done():
				t.Fatalf("Run deadlocked while draining queued child: %v", ctx.Err())
			}
			if got := queuedSideEffects.Load(); got != 0 {
				t.Fatalf("queued child side effects = %d, want 0", got)
			}
		})
	}
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

func TestExecutorInvokeKeepsNativeThenAfterGuestTampering(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`"real"`), Limits{})
	result, err := executor.Run(context.Background(), `
const promise = tools.invoke("ok");
Promise.prototype.then = function() { return Promise.resolve("forged"); };
return await promise;
`)
	if err != nil || string(result.JSON) != `"real"` {
		t.Fatalf("tampered native then = result:%s err:%v", result.JSON, err)
	}
}

func TestExecutorHostPanicIsFatalAndNotCatchable(t *testing.T) {
	executor := mustExecutor(t, HostFunc(func(context.Context, Invocation) (json.RawMessage, error) {
		panic("host bug")
	}), Limits{})
	_, err := executor.Run(context.Background(), `try { await tools.invoke("panic"); return "caught"; } catch (_) { return "swallowed"; }`)
	if err == nil || !strings.Contains(err.Error(), "code host invocation panicked") {
		t.Fatalf("host panic = %v, want fatal infrastructure error", err)
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
	if !caught.IsError || caught.Message != "normalized" || caught.Name != "ToolInvocationError" || caught.Code != invocationErrorCode || !caught.Value.IsError || len(caught.Value.Blocks) != 1 || caught.Value.Blocks[0].Text != "normalized" {
		t.Fatalf("caught host error = %s", result.JSON)
	}
	_, err = executor.Run(context.Background(), `await tools.invoke("fail");`)
	if err == nil || !strings.Contains(err.Error(), "normalized") || strings.Contains(err.Error(), "[object Object]") {
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

func TestExecutorCatalogSearchMatchesNaturalLanguageKeywords(t *testing.T) {
	entries := []CatalogEntry{
		{Name: "email", Description: "Read and send configured email messages."},
		{Name: "recally", Description: "Save and read web articles in the user's reading library."},
		{Name: "skills", Description: "Search, load, and read installed system skills."},
		{Name: "oauth", Description: "Connect external accounts such as GitHub with OAuth authentication."},
		{Name: "memory", Description: "Search and read durable user memories."},
		{Name: "goal", Description: "Create and manage durable goals."},
	}
	executor, err := NewExecutor(jsonHost(`null`), Limits{}, entries...)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Run(context.Background(), `
const first = query => tools.search(query)[0]?.name;
return {
  recally: first("saving and recalling web content articles"),
  skills: first("load a system skill"),
  oauth: first("connect a GitHub account with OAuth"),
  memory: first("search durable memories"),
  goal: first("create a durable goal")
};
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON), `{"recally":"recally","skills":"skills","oauth":"oauth","memory":"memory","goal":"goal"}`; got != want {
		t.Fatalf("natural-language search = %s, want %s", got, want)
	}
}

func TestExecutorCatalogSearchPaginatesWithinFixedPage(t *testing.T) {
	entries := make([]CatalogEntry, 21)
	for i := range entries {
		entries[i] = CatalogEntry{Name: fmt.Sprintf("tool-%02d", i)}
	}
	executor, err := NewExecutor(jsonHost(`null`), Limits{}, entries...)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Run(context.Background(), `
const first = tools.search("");
const second = tools.search("", first.nextOffset);
return { first: first.length, more: first.hasMore, second: second.length, next: first.nextOffset };
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON), `{"first":20,"more":true,"second":1,"next":20}`; got != want {
		t.Fatalf("catalog page = %s, want %s", got, want)
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

// TestExecutorOrdinaryScriptMistakes covers the shapes an average model-written
// script actually fails in. None of them is a host failure, so none may reach
// the caller as one.
func TestExecutorOrdinaryScriptMistakes(t *testing.T) {
	t.Run("syntax error is a JavaScript error", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`null`), Limits{})
		_, err := executor.Run(context.Background(), `return 1 +;`)
		if err == nil {
			t.Fatal("Run error = nil, want a JavaScript error")
		}
		if !strings.HasPrefix(err.Error(), "javascript execution failed:") {
			t.Fatalf("Run error = %q, want a javascript execution failure", err)
		}
		if !strings.Contains(err.Error(), "SyntaxError") {
			t.Fatalf("Run error = %q, want the SyntaxError detail", err)
		}
	})
	t.Run("no return value completes as null", func(t *testing.T) {
		var calls int
		executor := mustExecutor(t, HostFunc(func(_ context.Context, _ Invocation) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`"done"`), nil
		}), Limits{})
		result, err := executor.Run(context.Background(), `await tools.invoke("echo", {});`)
		if err != nil {
			t.Fatalf("Run error = %v, want nil", err)
		}
		if got := string(result.JSON); got != `null` {
			t.Fatalf("Run result = %s, want null", got)
		}
		if calls != 1 {
			t.Fatalf("child calls = %d, want 1", calls)
		}
	})
	t.Run("unserializable return value is a JavaScript error", func(t *testing.T) {
		executor := mustExecutor(t, jsonHost(`null`), Limits{})
		_, err := executor.Run(context.Background(), `return () => 1;`)
		if err == nil || !strings.Contains(err.Error(), "TypeError") {
			t.Fatalf("Run error = %v, want a TypeError", err)
		}
		if !strings.HasPrefix(err.Error(), "javascript execution failed:") {
			t.Fatalf("Run error = %q, want a javascript execution failure", err)
		}
	})
}

// TestExecutorSourceCannotEscapeTheWrapper pins the compile-source-as-data
// contract: unbalanced brackets are a syntax error, never guest statements that
// run before activate() publishes the VM to the watcher.
func TestExecutorSourceCannotEscapeTheWrapper(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, source := range map[string]string{
		// Balanced for the compilation wrapper.
		"one level": "return 1;\n}); (function(){ while (true) {} })(); (async function(){",
		// Balanced for the deeper validation wrapper instead.
		"two levels": "return 1;\n}}); (function(){ while (true) {} })(); (async function(){ if (false) {",
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			_, err := executor.Run(ctx, source)
			if err == nil || !strings.Contains(err.Error(), "SyntaxError") {
				t.Fatalf("Run error = %v, want a SyntaxError", err)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("escaped source ran for %v before failing", elapsed)
			}
		})
	}
}

// TestExecutorDetectsAStalledScript covers a script that awaits something no
// host call will ever settle. It must fail as a guest error immediately rather
// than burn the wall clock down to a timeout.
func TestExecutorDetectsAStalledScript(t *testing.T) {
	executor := mustExecutor(t, jsonHost(`null`), Limits{})
	start := time.Now()
	_, err := executor.Run(context.Background(), `await new Promise(() => {});`)
	if err == nil || !strings.Contains(err.Error(), "can never settle") {
		t.Fatalf("Run error = %v, want a stalled-script failure", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stalled script took %v to fail", elapsed)
	}
}
