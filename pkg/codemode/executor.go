package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/quickjs"
)

const (
	defaultSourceBytes = 100 << 10
	defaultMemoryBytes = 64 << 20
	defaultStackSlots  = 1024
	defaultResultBytes = 1 << 20
	defaultWallClock   = 30 * time.Second
	internalActivate   = "__stellaActivate"
	internalInvoke     = "__stellaInvoke"
	internalDone       = "__stellaDone"
	internalResult     = "__stellaResult"
	internalError      = "__stellaError"
)

var (
	// ErrSourceTooLarge reports source that exceeds Limits.SourceBytes.
	ErrSourceTooLarge = errors.New("code source exceeds limit")
	// ErrResultTooLarge reports serialized JavaScript output that exceeds Limits.ResultBytes.
	ErrResultTooLarge = errors.New("code result exceeds limit")
	// ErrCancelled reports cancellation before the script completed.
	ErrCancelled = errors.New("code execution cancelled")
	// ErrTimedOut reports expiry of the effective wall-clock limit.
	ErrTimedOut = errors.New("code execution timed out")
)

// Limits bounds a single isolated JavaScript execution. Zero values use the
// Phase 1 fixed defaults; they are intentionally not operator configuration.
type Limits struct {
	SourceBytes   int
	WallClock     time.Duration
	MemoryBytes   uintptr
	MaxStackSlots uintptr
	ResultBytes   int
}

func (l Limits) withDefaults() Limits {
	if l.SourceBytes == 0 {
		l.SourceBytes = defaultSourceBytes
	}
	if l.WallClock == 0 {
		l.WallClock = defaultWallClock
	}
	if l.MemoryBytes == 0 {
		l.MemoryBytes = defaultMemoryBytes
	}
	if l.MaxStackSlots == 0 {
		l.MaxStackSlots = defaultStackSlots
	}
	if l.ResultBytes == 0 {
		l.ResultBytes = defaultResultBytes
	}
	return l
}

// Invocation is a pure-Go request emitted by tools.invoke. Arguments contain
// JSON, never a QuickJS Value, so hosts cannot retain VM-owned memory.
type Invocation struct {
	ID        uint64
	Name      string
	Arguments json.RawMessage
}

// Host executes a child invocation outside the VM. It must honor ctx
// cancellation so Executor can join every child before the owner closes its VM.
type Host interface {
	Invoke(ctx context.Context, invocation Invocation) (json.RawMessage, error)
}

// HostFunc adapts a function to Host.
type HostFunc func(context.Context, Invocation) (json.RawMessage, error)

// Invoke implements Host.
func (f HostFunc) Invoke(ctx context.Context, invocation Invocation) (json.RawMessage, error) {
	return f(ctx, invocation)
}

// Result is the JSON-safe JavaScript value produced by the script.
type Result struct {
	JSON json.RawMessage
}

// Executor creates one QuickJS VM per Run. It is safe for concurrent Run calls
// when its Host is safe for concurrent Invoke calls.
type Executor struct {
	host   Host
	limits Limits
	hooks  *runHooks // test-only lifecycle observation; nil in production.
}

// NewExecutor constructs the concrete QuickJS executor. There is deliberately
// no engine interface: this phase validates exactly one runtime.
func NewExecutor(host Host, limits Limits) (*Executor, error) {
	if host == nil {
		return nil, errors.New("codemode host is required")
	}
	limits = limits.withDefaults()
	if limits.SourceBytes <= 0 || limits.WallClock <= 0 || limits.MemoryBytes == 0 || limits.MaxStackSlots == 0 || limits.ResultBytes <= 0 {
		return nil, errors.New("codemode limits must be positive")
	}
	return &Executor{host: host, limits: limits}, nil
}

// Run executes source in a new VM. All VM operations happen on one locked owner
// goroutine, except the binding-documented atomic VM.Interrupt call made at most
// once by the cancellation watcher after __stellaActivate publishes the VM.
func (e *Executor) Run(ctx context.Context, source string) (Result, error) {
	if len(source) > e.limits.SourceBytes {
		return Result{}, ErrSourceTooLarge
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resultCh := make(chan runResult, 1)
	go e.runOwner(ctx, source, resultCh)
	result := <-resultCh
	return result.result, result.err
}

type runResult struct {
	result Result
	err    error
}

type executionState uint8

const (
	stateIdle executionState = iota
	stateRunning
	stateCancelRequested
	stateReturned
	stateDraining
	stateClosed
)

type runHooks struct {
	beforeActivate     func()
	onInterrupt        func()
	onState            func(executionState)
	afterWatcherJoined func()
}

type runControl struct {
	active      chan struct{}
	stopWatch   chan struct{}
	watchDone   chan struct{}
	activeOnce  sync.Once
	stopOnce    sync.Once
	cancelled   atomic.Bool
	interrupted atomic.Bool
	onInterrupt func()
}

func newRunControl() *runControl {
	return &runControl{
		active:    make(chan struct{}),
		stopWatch: make(chan struct{}),
		watchDone: make(chan struct{}),
	}
}

func (c *runControl) publishActive() {
	c.activeOnce.Do(func() { close(c.active) })
}

func (c *runControl) stopWatcher() {
	c.stopOnce.Do(func() { close(c.stopWatch) })
}

type completion struct {
	id     uint64
	result json.RawMessage
	err    error
}

type pendingPromise struct {
	capability *quickjs.PromiseCapability
}

func (e *Executor) runOwner(parent context.Context, source string, out chan<- runResult) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var final runResult
	defer func() { out <- final }()

	ctx, cancelRun := context.WithTimeout(parent, e.limits.WallClock)
	defer cancelRun()

	vm, err := quickjs.NewVM()
	if err != nil {
		final = runResult{err: err}
		return
	}

	control := newRunControl()
	control.onInterrupt = e.onInterrupt
	e.transition(stateIdle)
	go watchCancellation(ctx, vm, control)

	childCtx, cancelChildren := context.WithCancel(context.Background())
	completions := make(chan completion)
	discardCompletions := make(chan struct{})
	var childWG sync.WaitGroup
	accepting := true
	defer func() {
		// No callback can enqueue another child once the owner has returned.
		accepting = false

		// This order is the lifecycle contract. The watcher is joined before Close,
		// making its one external atomic write impossible after interruptData is freed.
		control.stopWatcher()
		<-control.watchDone
		e.afterWatcherJoined()
		e.transition(stateDraining)

		cancelChildren()
		close(discardCompletions)
		childWG.Wait()

		// The owner alone releases resolver Values and closes the VM.
		_ = vm.Close()
		e.transition(stateClosed)
	}()

	vm.SetMemoryLimit(e.limits.MemoryBytes)
	vm.SetMaxStackSize(e.limits.MaxStackSlots)
	if err := vm.SetEvalTimeout(e.limits.WallClock); err != nil {
		final = runResult{err: err}
		return
	}

	var (
		nextID  uint64
		pending = map[uint64]pendingPromise{}
	)
	defer func() {
		for _, promise := range pending {
			promise.capability.Resolve.Free()
			promise.capability.Reject.Free()
		}
	}()

	if err := vm.RegisterHostFunc(internalActivate, func(_ []any) (any, error) {
		e.beforeActivate()
		control.publishActive()
		e.transition(stateRunning)
		return nil, nil
	}); err != nil {
		final = runResult{err: err}
		return
	}

	if err := vm.RegisterFunc(internalInvoke, func(name string, values ...quickjs.Value) quickjs.Value {
		capability, err := vm.NewPromiseCapability()
		if err != nil {
			return quickjs.UndefinedValue
		}
		if !accepting {
			_, _ = capability.Reject.Call(quickjs.UndefinedValue, "code execution is stopping")
			capability.Resolve.Free()
			capability.Reject.Free()
			return capability.Promise
		}

		var rawArgs json.RawMessage = []byte("null")
		if len(values) > 0 {
			encoded, err := values[0].MarshalJSON()
			if err != nil {
				_, _ = capability.Reject.Call(quickjs.UndefinedValue, "serialize tools.invoke arguments: "+err.Error())
				capability.Resolve.Free()
				capability.Reject.Free()
				return capability.Promise
			}
			rawArgs = encoded
		}

		nextID++
		id := nextID
		pending[id] = pendingPromise{capability: capability}
		// quickjs' string conversion can borrow VM memory. The child outlives this
		// callback, so it receives only copied Go data that remains valid after Close.
		stableName := strings.Clone(name)
		stableArgs := json.RawMessage(bytes.Clone(rawArgs))
		childWG.Go(func() {
			result, err := e.host.Invoke(childCtx, Invocation{ID: id, Name: stableName, Arguments: stableArgs})
			completion := completion{id: id, result: result, err: err}
			select {
			case completions <- completion:
			case <-discardCompletions:
			}
		})

		return capability.Promise
	}, false); err != nil {
		final = runResult{err: err}
		return
	}

	if _, err := vm.Eval(`globalThis.tools = Object.freeze({ invoke: `+internalInvoke+` });`, quickjs.EvalGlobal); err != nil {
		final = runResult{err: err}
		return
	}

	// Activation is the first evaluated instruction after QuickJS configures its
	// interrupt deadline. A pre-cancelled watcher therefore cannot be erased by
	// Eval's setup before it gets its single, documented atomic Interrupt call.
	wrapped := `globalThis.` + internalDone + ` = false;
globalThis.` + internalResult + ` = undefined;
globalThis.` + internalError + ` = undefined;
void (async () => {
  ` + internalActivate + `();
  try {
    const value = await (async () => {
` + source + `
    })();
    try { globalThis.` + internalResult + ` = JSON.stringify(value); } catch (e) { globalThis.` + internalError + ` = String(e) + "\\n" + String(e && e.stack || ""); }
  } catch (error) {
    globalThis.` + internalError + ` = String(error) + "\\n" + String(error && error.stack || "");
  }
  globalThis.` + internalDone + ` = true;
})();`

	if _, err := vm.Eval(wrapped, quickjs.EvalGlobal); err != nil {
		if control.cancelled.Load() {
			e.transition(stateCancelRequested)
		}
		e.transition(stateReturned)
		final = runResult{err: e.classify(ctx, err)}
		return
	}

	for {
		if _, err := vm.ExecutePendingJobs(); err != nil {
			if control.cancelled.Load() {
				e.transition(stateCancelRequested)
			}
			e.transition(stateReturned)
			final = runResult{err: e.classify(ctx, err)}
			return
		}
		done, err := vm.Eval(`globalThis.`+internalDone, quickjs.EvalGlobal)
		if err != nil {
			if control.cancelled.Load() {
				e.transition(stateCancelRequested)
			}
			e.transition(stateReturned)
			final = runResult{err: e.classify(ctx, err)}
			return
		}
		if done == true {
			if control.cancelled.Load() {
				e.transition(stateCancelRequested)
			}
			e.transition(stateReturned)
			result, err := e.readResult(vm)
			final = runResult{result: result, err: e.classify(ctx, err)}
			return
		}

		select {
		case <-ctx.Done():
			accepting = false
			e.transition(stateCancelRequested)
			e.transition(stateReturned)
			final = runResult{err: e.classify(ctx, ctx.Err())}
			return
		case received := <-completions:
			if !accepting {
				continue
			}
			promise, ok := pending[received.id]
			if !ok {
				continue
			}
			delete(pending, received.id)
			var settleErr error
			if received.err != nil {
				_, settleErr = promise.capability.Reject.Call(quickjs.UndefinedValue, received.err.Error())
			} else {
				var value any
				if decodeErr := json.Unmarshal(received.result, &value); decodeErr != nil {
					settleErr = fmt.Errorf("decode host completion: %w", decodeErr)
				} else {
					_, settleErr = promise.capability.Resolve.Call(quickjs.UndefinedValue, value)
				}
			}
			promise.capability.Resolve.Free()
			promise.capability.Reject.Free()
			if settleErr != nil {
				e.transition(stateReturned)
				final = runResult{err: e.classify(ctx, settleErr)}
				return
			}
		}
	}
}

func watchCancellation(ctx context.Context, vm *quickjs.VM, control *runControl) {
	defer close(control.watchDone)
	select {
	case <-ctx.Done():
		control.cancelled.Store(true)
	case <-control.stopWatch:
		return
	}

	select {
	case <-control.active:
		// modernc.org/quickjs documents this atomic store as safe while Eval is
		// running. It is the sole VM operation performed outside the owner.
		if control.interrupted.CompareAndSwap(false, true) {
			vm.Interrupt()
			if control.onInterrupt != nil {
				control.onInterrupt()
			}
		}
	case <-control.stopWatch:
	}
}

func (e *Executor) transition(state executionState) {
	if e.hooks != nil && e.hooks.onState != nil {
		e.hooks.onState(state)
	}
}

func (e *Executor) beforeActivate() {
	if e.hooks != nil && e.hooks.beforeActivate != nil {
		e.hooks.beforeActivate()
	}
}

func (e *Executor) afterWatcherJoined() {
	if e.hooks != nil && e.hooks.afterWatcherJoined != nil {
		e.hooks.afterWatcherJoined()
	}
}

func (e *Executor) onInterrupt() {
	if e.hooks != nil && e.hooks.onInterrupt != nil {
		e.hooks.onInterrupt()
	}
}

func (e *Executor) readResult(vm *quickjs.VM) (Result, error) {
	if message, err := vm.Eval(`globalThis.`+internalError, quickjs.EvalGlobal); err != nil {
		return Result{}, err
	} else if text, ok := message.(string); ok {
		return Result{}, fmt.Errorf("javascript execution failed: %s", text)
	}
	raw, err := vm.Eval(`globalThis.`+internalResult, quickjs.EvalGlobal)
	if err != nil {
		return Result{}, err
	}
	result, ok := raw.(string)
	if !ok {
		return Result{}, errors.New("javascript execution returned no JSON result")
	}
	if len(result) > e.limits.ResultBytes {
		return Result{}, ErrResultTooLarge
	}
	return Result{JSON: json.RawMessage(result)}, nil
}

func (e *Executor) classify(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimedOut
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ErrCancelled
	}
	if err != nil && strings.Contains(err.Error(), "interrupted") {
		return ErrTimedOut
	}
	return err
}
