package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
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
	internalCheckpoint = "__stellaCheckpoint"
	internalComplete   = "__stellaComplete"
	internalFail       = "__stellaFail"
	childQueueSize     = 64
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
	active     chan struct{}
	stopWatch  chan struct{}
	watchDone  chan struct{}
	activeOnce sync.Once
	stopOnce   sync.Once
	// cancelRequested is deliberately independent from QuickJS' interrupt bit.
	// The binding reinitializes that bit at each Eval/Call/job entry, while this
	// bit makes cancellation survive those entries and lets the owner short-circuit.
	cancelRequested atomic.Bool
	cancelCause     atomic.Int32
	interrupted     atomic.Bool
	onInterrupt     func()
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

type childRequest struct {
	id         uint64
	invocation Invocation
}

type executionResult struct {
	done bool
	json json.RawMessage
	err  error
}

const (
	cancelNone int32 = iota
	cancelledByParent
	cancelledByDeadline
)

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
	deadline := time.Now().Add(e.limits.WallClock)

	vm, err := quickjs.NewVM()
	if err != nil {
		final = runResult{err: err}
		return
	}

	control := newRunControl()
	control.onInterrupt = e.onInterrupt
	e.transition(stateIdle)
	go watchCancellation(ctx, vm, control)

	// Child lifetime inherits the caller's cancellation and the fixed execution
	// deadline. Teardown still calls cancelChildren so owner return never waits
	// for the deadline to propagate.
	childCtx, cancelChildren := context.WithCancel(ctx)
	requests := make(chan childRequest, childQueueSize)
	completions := make(chan completion)
	discardCompletions := make(chan struct{})
	var childWG sync.WaitGroup
	accepting := true
	childWG.Go(func() {
		for {
			select {
			case <-childCtx.Done():
				return
			case request := <-requests:
				if childCtx.Err() != nil {
					return
				}
				result, err := e.host.Invoke(childCtx, request.invocation)
				completed := completion{id: request.id, result: bytes.Clone(result), err: err}
				select {
				case completions <- completed:
				case <-discardCompletions:
				}
			}
		}
	})
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
		control.observe(ctx)
		if err := control.cancellationError(); err != nil {
			return nil, err
		}
		return nil, nil
	}); err != nil {
		final = runResult{err: err}
		return
	}

	if err := vm.RegisterFunc(internalInvoke, func(name string, values ...quickjs.Value) quickjs.Value {
		control.observe(ctx)
		if !accepting || control.cancellationError() != nil {
			// The owner will return the durable cancellation result. Do not enter a
			// resolver Call here, because that binding entry would re-arm its
			// interrupt bit after the watcher has requested cancellation.
			return quickjs.UndefinedValue
		}
		capability, err := vm.NewPromiseCapability()
		if err != nil {
			return quickjs.UndefinedValue
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
		stableName := string(bytes.Clone([]byte(name)))
		stableArgs := json.RawMessage(bytes.Clone(rawArgs))
		// The callback never waits for the child. One worker consumes this FIFO,
		// so Promise.all cannot create concurrent host invocations.
		select {
		case requests <- childRequest{id: id, invocation: Invocation{ID: id, Name: stableName, Arguments: stableArgs}}:
		default:
			delete(pending, id)
			control.observe(ctx)
			if control.cancellationError() == nil {
				_, _ = capability.Reject.Call(quickjs.UndefinedValue, "code invocation queue is full")
			}
			capability.Resolve.Free()
			capability.Reject.Free()
		}

		return capability.Promise
	}, false); err != nil {
		final = runResult{err: err}
		return
	}

	if err := vm.RegisterHostFunc(internalCheckpoint, func(_ []any) (any, error) {
		control.observe(ctx)
		return nil, control.cancellationError()
	}); err != nil {
		final = runResult{err: err}
		return
	}
	var finished executionResult
	if err := vm.RegisterFunc(internalComplete, func(value quickjs.Value) quickjs.Value {
		if finished.done {
			return quickjs.UndefinedValue
		}
		finished.done = true
		raw, err := value.MarshalJSON()
		if err != nil {
			finished.err = fmt.Errorf("serialize JavaScript result: %w", err)
			return quickjs.UndefinedValue
		}
		if !json.Valid(raw) {
			finished.err = errors.New("serialize JavaScript result: invalid JSON")
			return quickjs.UndefinedValue
		}
		if len(raw) > e.limits.ResultBytes {
			finished.err = ErrResultTooLarge
			return quickjs.UndefinedValue
		}
		finished.json = json.RawMessage(bytes.Clone(raw))
		return quickjs.UndefinedValue
	}, false); err != nil {
		final = runResult{err: err}
		return
	}
	if err := vm.RegisterFunc(internalFail, func(value quickjs.Value) quickjs.Value {
		if !finished.done {
			finished.done = true
			jsErr := quickjs.ErrorFromValue(value)
			if jsErr.Name == "InternalError" {
				// quickjs' configured interrupt reaches an async wrapper here as an
				// exception value, rather than as the Eval/Call Go error path.
				finished.err = ErrTimedOut
			} else {
				finished.err = fmt.Errorf("javascript execution failed: %s", jsErr.Error())
			}
		}
		return quickjs.UndefinedValue
	}, false); err != nil {
		final = runResult{err: err}
		return
	}

	// Bootstrap closes over all control functions then removes their global
	// names. Script code can define properties with those names, but can neither
	// alter this closure nor write the host-owned completion state.
	bootstrap := `(function() {
  const activate = globalThis.` + internalActivate + `;
  const invoke = globalThis.` + internalInvoke + `;
  const checkpoint = globalThis.` + internalCheckpoint + `;
  const complete = globalThis.` + internalComplete + `;
  const fail = globalThis.` + internalFail + `;
  delete globalThis.` + internalActivate + `;
  delete globalThis.` + internalInvoke + `;
  delete globalThis.` + internalCheckpoint + `;
  delete globalThis.` + internalComplete + `;
  delete globalThis.` + internalFail + `;
  Object.defineProperty(globalThis, "tools", { value: Object.freeze({
    invoke: (...args) => invoke(...args).then(
      value => { checkpoint(); return value; },
      error => { checkpoint(); throw error; }
    )
  }), writable: false, configurable: false });
  return async function() {
    activate();
    try {
      const value = await (async () => {
` + source + `
      })();
      complete(value);
    } catch (error) {
      fail(error);
    }
  };
})()`
	control.observe(ctx)
	if err := control.cancellationError(); err != nil {
		e.transition(stateCancelRequested)
		e.transition(stateReturned)
		final = runResult{err: err}
		return
	}
	runner, err := vm.EvalValue(bootstrap, quickjs.EvalGlobal)
	if err != nil {
		e.transition(stateReturned)
		final = runResult{err: e.classify(ctx, control, deadline, err)}
		return
	}
	defer runner.Free()
	control.observe(ctx)
	if err := control.cancellationError(); err != nil {
		e.transition(stateCancelRequested)
		e.transition(stateReturned)
		final = runResult{err: err}
		return
	}
	if _, err := runner.Call(quickjs.UndefinedValue); err != nil {
		if control.cancelRequested.Load() {
			e.transition(stateCancelRequested)
		}
		e.transition(stateReturned)
		final = runResult{err: e.classify(ctx, control, deadline, err)}
		return
	}

	for {
		control.observe(ctx)
		if err := control.cancellationError(); err != nil {
			e.transition(stateCancelRequested)
			e.transition(stateReturned)
			final = runResult{err: err}
			return
		}
		if _, err := vm.ExecutePendingJobs(); err != nil {
			if control.cancelRequested.Load() {
				e.transition(stateCancelRequested)
			}
			e.transition(stateReturned)
			final = runResult{err: e.classify(ctx, control, deadline, err)}
			return
		}
		if finished.done {
			control.observe(ctx)
			if control.cancelRequested.Load() {
				e.transition(stateCancelRequested)
			}
			e.transition(stateReturned)
			switch {
			case control.cancellationError() != nil:
				final = runResult{err: control.cancellationError()}
			case finished.err != nil:
				final = runResult{err: finished.err}
			case !json.Valid(finished.json):
				final = runResult{err: errors.New("javascript execution returned invalid JSON")}
			default:
				final = runResult{result: Result{JSON: bytes.Clone(finished.json)}}
			}
			return
		}

		select {
		case <-ctx.Done():
			accepting = false
			control.observe(ctx)
			e.transition(stateCancelRequested)
			e.transition(stateReturned)
			final = runResult{err: control.cancellationError()}
			return
		case received := <-completions:
			if !accepting {
				continue
			}
			promise, ok := pending[received.id]
			if !ok {
				continue
			}
			control.observe(ctx)
			if err := control.cancellationError(); err != nil {
				e.transition(stateCancelRequested)
				e.transition(stateReturned)
				final = runResult{err: err}
				return
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
				final = runResult{err: e.classify(ctx, control, deadline, settleErr)}
				return
			}
		}
	}
}

func watchCancellation(ctx context.Context, vm *quickjs.VM, control *runControl) {
	defer close(control.watchDone)
	select {
	case <-ctx.Done():
		control.observe(ctx)
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

func (c *runControl) observe(ctx context.Context) {
	if ctx.Err() == nil {
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		c.cancelCause.CompareAndSwap(cancelNone, cancelledByDeadline)
	} else {
		c.cancelCause.CompareAndSwap(cancelNone, cancelledByParent)
	}
	c.cancelRequested.Store(true)
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

func (c *runControl) cancellationError() error {
	switch c.cancelCause.Load() {
	case cancelledByDeadline:
		return ErrTimedOut
	case cancelledByParent:
		return ErrCancelled
	}
	return nil
}

func (e *Executor) classify(ctx context.Context, control *runControl, deadline time.Time, err error) error {
	if cancellation := control.cancellationError(); cancellation != nil {
		return cancellation
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimedOut
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ErrCancelled
	}
	// quickjs emits its VM interrupt as InternalError. This is an exception
	// source classification, never a search for an "interrupted" message; a
	// user-thrown Error("interrupted") remains an ordinary JavaScript error.
	var jsErr *quickjs.Error
	if errors.As(err, &jsErr) && jsErr.Name == "InternalError" {
		return ErrTimedOut
	}
	return err
}
