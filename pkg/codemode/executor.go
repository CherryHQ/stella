package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/quickjs"
)

const (
	defaultSourceBytes  = 100 << 10
	defaultMemoryBytes  = 64 << 20
	defaultStackSlots   = 1024
	defaultPayloadBytes = 1 << 20
	defaultResultBytes  = 1 << 20
	defaultWallClock    = 30 * time.Second
	defaultMaxCalls     = 64
	defaultLogEntries   = 256
	defaultLogBytes     = 256 << 10
	internalActivate    = "__stellaActivate"
	internalInvoke      = "__stellaInvoke"
	internalSearch      = "__stellaSearch"
	internalDescribe    = "__stellaDescribe"
	internalCheckpoint  = "__stellaCheckpoint"
	internalLog         = "__stellaLog"
	internalComplete    = "__stellaComplete"
	internalFail        = "__stellaFail"
	childQueueSize      = 64
	catalogResultLimit  = 20
	invocationErrorCode = "tool_invocation_failed"
	childWatchdogGrace  = 5 * time.Second
)

var (
	// activeVMSem is a process-wide ceiling. Keep it global while code mode is
	// low volume; split it per tenant or pool only if throughput proves it matters.
	activeVMSem = make(chan struct{}, 4)

	// ErrSourceTooLarge reports source that exceeds Limits.SourceBytes.
	ErrSourceTooLarge = errors.New("code source exceeds limit")
	// ErrResultTooLarge reports serialized JavaScript output that exceeds Limits.ResultBytes.
	ErrResultTooLarge = errors.New("code result exceeds limit")
	// ErrPayloadTooLarge reports a child invocation payload or completion that
	// exceeds Limits.PayloadBytes.
	ErrPayloadTooLarge = errors.New("code payload exceeds limit")
	// ErrInvocationLimit reports a script that attempts more child calls than
	// Limits.MaxCalls permits.
	ErrInvocationLimit = errors.New("code invocation count exceeds limit")
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
	PayloadBytes  int
	ResultBytes   int
	MaxCalls      int
	LogEntries    int
	LogBytes      int
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
	if l.PayloadBytes == 0 {
		l.PayloadBytes = defaultPayloadBytes
	}
	if l.MaxCalls == 0 {
		l.MaxCalls = defaultMaxCalls
	}
	if l.LogEntries == 0 {
		l.LogEntries = defaultLogEntries
	}
	if l.LogBytes == 0 {
		l.LogBytes = defaultLogBytes
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

// CatalogEntry is copied into one Executor before Run starts. Discovery reads
// this execution-local data only, so it cannot block the VM owner on a host
// callback or observe a catalog that changes during a turn.
type CatalogEntry struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Result is the JSON-safe JavaScript value produced by the script.
type Result struct {
	JSON json.RawMessage
}

// InvocationError rejects tools.invoke with JSON rather than a string. It is
// used by bridges that need a caught child failure to retain structured data.
type InvocationError struct {
	Value json.RawMessage
	Err   error
}

func (e *InvocationError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "tool invocation failed"
}

// Executor creates one QuickJS VM per Run. It is safe for concurrent Run calls
// when its Host is safe for concurrent Invoke calls.
type Executor struct {
	host    Host
	limits  Limits
	catalog []CatalogEntry
	hooks   *runHooks // test-only lifecycle observation; nil in production.
}

// NewExecutor constructs the concrete QuickJS executor. There is deliberately
// no engine interface: this phase validates exactly one runtime.
func NewExecutor(host Host, limits Limits, catalog ...CatalogEntry) (*Executor, error) {
	if host == nil {
		return nil, errors.New("codemode host is required")
	}
	limits = limits.withDefaults()
	if limits.SourceBytes <= 0 || limits.WallClock <= 0 || limits.MemoryBytes == 0 || limits.MaxStackSlots == 0 || limits.PayloadBytes <= 0 || limits.ResultBytes <= 0 || limits.MaxCalls <= 0 || limits.LogEntries <= 0 || limits.LogBytes <= 0 {
		return nil, errors.New("codemode limits must be positive")
	}
	return &Executor{host: host, limits: limits, catalog: cloneCatalog(catalog)}, nil
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
	// The wall-clock budget begins at admission, not VM construction. Queueing
	// behind the process-wide semaphore is still part of this execution.
	deadline := time.Now().Add(e.limits.WallClock)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := acquireVM(runCtx); err != nil {
		return Result{}, err
	}
	defer func() { <-activeVMSem }()

	resultCh := make(chan runResult, 1)
	go e.runOwner(runCtx, source, deadline, resultCh)
	result := <-resultCh
	return result.result, result.err
}

func acquireVM(ctx context.Context) error {
	if err := executionContextError(ctx); err != nil {
		return err
	}
	select {
	case activeVMSem <- struct{}{}:
		// If cancellation raced with an available permit, do not create a VM.
		if err := executionContextError(ctx); err != nil {
			<-activeVMSem
			return err
		}
		return nil
	case <-ctx.Done():
		return executionContextError(ctx)
	}
}

func executionContextError(ctx context.Context) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ErrTimedOut
	case errors.Is(ctx.Err(), context.Canceled):
		return ErrCancelled
	default:
		return nil
	}
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
	beforeActivate      func()
	onInterrupt         func()
	onState             func(executionState)
	afterWatcherJoined  func()
	afterInvokeEnqueued func()
	onOwnerFatal        func()
	onWorkerFatal       func()
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
	fatal  error
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

// fatalLatch is the only owner/worker shared execution state. It deliberately
// carries Go errors only; the worker never reads or writes VM-owned values.
type fatalLatch struct {
	mu           sync.Mutex
	err          error
	stopChildren bool
}

func (l *fatalLatch) set(err error) bool {
	if err == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Any fatal source closes admission, including an owner callback failure.
	// A queued request may otherwise start after its outcome is already known.
	l.stopChildren = true
	if l.err == nil {
		l.err = err
		return true
	}
	return false
}

func (l *fatalLatch) setWorker(err error) bool {
	if err == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// A worker fatal closes the child start gate even when an earlier owner
	// fatal already won error classification. Requests queued before either
	// fatal must drain without gaining a later host side effect.
	l.stopChildren = true
	if l.err != nil {
		return false
	}
	l.err = err
	return true
}

func (l *fatalLatch) get() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *fatalLatch) childrenStopped() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopChildren
}

type invocationRejection struct {
	Message string `json:"message"`
	Code    string `json:"code"`
	Value   any    `json:"value,omitempty"`
}

func (e *Executor) runOwner(ctx context.Context, source string, deadline time.Time, out chan<- runResult) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var final runResult
	defer func() { out <- final }()

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
	fatal := &fatalLatch{}
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
				if fatal.childrenStopped() {
					// Requests admitted just before another completion latched fatal
					// must drain without starting another child side effect.
					select {
					case completions <- completion{id: request.id, fatal: fatal.get()}:
					case <-discardCompletions:
					}
					continue
				}
				result, err := invokeHost(childCtx, e.host, request.invocation)
				completed := completion{id: request.id}
				switch {
				case err != nil:
					if childCtx.Err() != nil {
						// Outer cancellation owns classification. A cooperative child
						// returning its context error must not overwrite it as infra.
						break
					}
					var invocation *InvocationError
					if errors.As(err, &invocation) {
						if len(invocation.Value) > e.limits.PayloadBytes || (len(invocation.Value) != 0 && !json.Valid(invocation.Value)) {
							completed.fatal = ErrPayloadTooLarge
						} else {
							completed.err = err
						}
					} else {
						// Native execution treats lifecycle, canonicalization, and bridge
						// failures as infrastructure failures. They must terminate the
						// outer code call too, never become script-catchable tool errors.
						completed.fatal = err
					}
				case len(result) > e.limits.PayloadBytes || !json.Valid(result):
					// Do not copy an oversized bridge result into the owner queue.
					// This is a hard execution limit, not a script-catchable tool
					// failure, because the bridge cannot safely materialize it.
					completed.fatal = ErrPayloadTooLarge
				default:
					completed.result = bytes.Clone(result)
				}
				if completed.fatal != nil {
					// Publish the admission close before the owner can run another
					// guest microtask. This is pure Go synchronization, not VM access.
					if fatal.setWorker(completed.fatal) {
						e.onWorkerFatal()
					}
				}
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
		watchdog := time.AfterFunc(childWatchdogGrace, func() {
			// Go cannot kill a blocked tool goroutine. This is deliberately a
			// detection point, while the Host contract remains cooperative.
			slog.Error("code mode child ignored cancellation; waiting for host", "deadline", deadline.UTC().Format(time.RFC3339))
		})
		childWG.Wait()
		watchdog.Stop()

		// The owner alone releases resolver Values and closes the VM.
		_ = vm.Close()
		e.transition(stateClosed)
	}()

	vm.SetMemoryLimit(e.limits.MemoryBytes)
	vm.SetMaxStackSize(e.limits.MaxStackSlots)
	var (
		nextID   uint64
		pending  = map[uint64]pendingPromise{}
		logCount int
		logBytes int
	)
	setFatal := func(err error) {
		if fatal.set(err) {
			e.onOwnerFatal()
		}
	}
	fatalErr := fatal.get
	releasePromise := func(pending pendingPromise) {
		pending.capability.Resolve.Free()
		pending.capability.Reject.Free()
	}
	defer func() {
		for _, promise := range pending {
			releasePromise(promise)
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
		if !accepting || fatalErr() != nil || control.cancellationError() != nil {
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
				setFatal(fmt.Errorf("serialize tools.invoke arguments: %w", err))
				releasePromise(pendingPromise{capability: capability})
				return quickjs.UndefinedValue
			}
			rawArgs = encoded
		}
		if len(rawArgs) > e.limits.PayloadBytes {
			setFatal(ErrPayloadTooLarge)
			releasePromise(pendingPromise{capability: capability})
			return quickjs.UndefinedValue
		}
		if nextID >= uint64(e.limits.MaxCalls) {
			setFatal(ErrInvocationLimit)
			releasePromise(pendingPromise{capability: capability})
			return quickjs.UndefinedValue
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
			e.afterInvokeEnqueued()
		default:
			delete(pending, id)
			setFatal(ErrInvocationLimit)
			releasePromise(pendingPromise{capability: capability})
		}

		return capability.Promise
	}, false); err != nil {
		final = runResult{err: err}
		return
	}
	if err := vm.RegisterHostFunc(internalSearch, func(args []any) (any, error) {
		if fatalErr() != nil {
			return nil, fatalErr()
		}
		query, offset, err := catalogSearchArgument(args)
		if err != nil {
			return nil, err
		}
		if len(query) > e.limits.PayloadBytes {
			setFatal(ErrPayloadTooLarge)
			return nil, ErrPayloadTooLarge
		}
		page := searchCatalog(e.catalog, query, offset)
		if err := boundedCatalogResponse(page, e.limits.PayloadBytes); err != nil {
			setFatal(err)
			return nil, err
		}
		return page, nil
	}); err != nil {
		final = runResult{err: err}
		return
	}
	// Console entries have no reader: they are counted to bound VM work and then
	// discarded. Exceeding the budget therefore drops the entry instead of
	// failing the execution — a chatty script is not a broken one.
	if err := vm.RegisterFunc(internalLog, func(values ...quickjs.Value) quickjs.Value {
		if fatalErr() != nil {
			return quickjs.UndefinedValue
		}
		if logCount >= e.limits.LogEntries {
			return quickjs.UndefinedValue
		}
		entryBytes := 0
		for _, value := range values {
			raw, err := value.MarshalJSON()
			if err != nil {
				// An unserializable console argument is a guest mistake, not a host
				// failure. Drop it like any other over-budget entry.
				return quickjs.UndefinedValue
			}
			entryBytes += len(raw)
		}
		if logBytes+entryBytes > e.limits.LogBytes {
			return quickjs.UndefinedValue
		}
		logCount++
		logBytes += entryBytes
		return quickjs.UndefinedValue
	}, false); err != nil {
		final = runResult{err: err}
		return
	}
	if err := vm.RegisterHostFunc(internalDescribe, func(args []any) (any, error) {
		if fatalErr() != nil {
			return nil, fatalErr()
		}
		name, err := catalogArgument(args, "describe")
		if err != nil {
			return nil, err
		}
		if len(name) > e.limits.PayloadBytes {
			setFatal(ErrPayloadTooLarge)
			return nil, ErrPayloadTooLarge
		}
		description, err := describeCatalog(e.catalog, name)
		if err != nil {
			return nil, err
		}
		if err := boundedCatalogResponse(description, e.limits.PayloadBytes); err != nil {
			setFatal(err)
			return nil, err
		}
		return description, nil
	}); err != nil {
		final = runResult{err: err}
		return
	}

	if err := vm.RegisterHostFunc(internalCheckpoint, func(_ []any) (any, error) {
		control.observe(ctx)
		if fatalErr() != nil {
			return nil, fatalErr()
		}
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
			setFatal(fmt.Errorf("serialize JavaScript result: %w", err))
			return quickjs.UndefinedValue
		}
		// JSON.stringify yields the literal "undefined" for a script that returns
		// nothing. That is the most common shape an LLM writes, so it completes as
		// null instead of failing the whole call and discarding child results.
		if string(raw) == "undefined" {
			raw = []byte("null")
		}
		if !json.Valid(raw) {
			setFatal(errors.New("serialize JavaScript result: invalid JSON"))
			return quickjs.UndefinedValue
		}
		if len(raw) > e.limits.ResultBytes {
			setFatal(ErrResultTooLarge)
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
			if jsErr.Name == "InternalError" && !time.Now().Before(deadline) {
				// quickjs' configured interrupt reaches an async wrapper here as an
				// exception value, rather than as the Eval/Call Go error path. A
				// user-created InternalError before the absolute deadline is ordinary.
				control.expireDeadline()
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

	// Bootstrap retains host controls in a closure that is never shared with the
	// user program. User source is compiled by the returned entry point via the
	// AsyncFunction constructor, which evaluates it in global scope, so it cannot
	// forge completion by naming complete/fail/invoke.
	bootstrap := `(function() {
  const activate = globalThis.` + internalActivate + `;
  const invoke = globalThis.` + internalInvoke + `;
	  const search = globalThis.` + internalSearch + `;
  const describe = globalThis.` + internalDescribe + `;
  const checkpoint = globalThis.` + internalCheckpoint + `;
	  const log = globalThis.` + internalLog + `;
  const complete = globalThis.` + internalComplete + `;
  const fail = globalThis.` + internalFail + `;
  delete globalThis.` + internalActivate + `;
  delete globalThis.` + internalInvoke + `;
	  delete globalThis.` + internalSearch + `;
  delete globalThis.` + internalDescribe + `;
  delete globalThis.` + internalCheckpoint + `;
	  delete globalThis.` + internalLog + `;
  delete globalThis.` + internalComplete + `;
  delete globalThis.` + internalFail + `;
  const childCalls = new Set();
  const childStates = new WeakMap();
  const nativeResolve = Promise.resolve.bind(Promise);
  const nativeThen = Promise.prototype.then;
  const nativeAll = Promise.all.bind(Promise);
  // QuickJS async assimilation consults this slot after guest code returns.
  // Preserve the native intrinsic rather than letting a guest replacement turn
  // owner bookkeeping into an unbounded self-resolution chain.
  Object.defineProperty(Promise.prototype, "then", { value: nativeThen, writable: false, configurable: false });
  const promiseThen = (promise, onFulfilled, onRejected) => Reflect.apply(nativeThen, promise, [onFulfilled, onRejected]);
  const trackPromise = (promise, state) => {
    Object.setPrototypeOf(promise, TrackedPromise.prototype);
    childStates.set(promise, state);
    return promise;
  };
  class TrackedPromise extends Promise {
    then(onFulfilled, onRejected) {
      const state = childStates.get(this);
      if (state && typeof onRejected === "function") state.observed = true;
      return trackPromise(promiseThen(this, onFulfilled, onRejected), state);
    }
    catch(onRejected) {
      return this.then(undefined, onRejected);
    }
    finally(onFinally) {
      const state = childStates.get(this);
      const runFinally = () => nativeResolve(typeof onFinally === "function" ? onFinally() : undefined);
      return trackPromise(promiseThen(this,
        value => promiseThen(runFinally(), () => value),
        error => promiseThen(runFinally(), () => { throw error; })
      ), state);
    }
  }
  const invocationError = failure => {
    const message = failure && typeof failure.message === "string" ? failure.message : String(failure);
    const error = new Error(message);
    Object.defineProperties(error, {
      name: { value: "ToolInvocationError", enumerable: true },
      code: { value: failure && failure.code || "` + invocationErrorCode + `", enumerable: true },
      value: { value: failure && failure.value, enumerable: true }
    });
    return error;
  };
  const trackedInvoke = (...args) => {
    const state = { settled: false, failure: undefined, observed: false, promise: undefined, then: undefined };
    const promise = trackPromise(promiseThen(invoke(...args),
      value => { checkpoint(); state.settled = true; return value; },
      failure => { checkpoint(); state.settled = true; state.failure = invocationError(failure); throw state.failure; }
    ), state);
    state.promise = promise;
    // Drain via the native method so this bookkeeping handler is never
    // mistaken for source-level rejection observation.
    state.then = (onFulfilled, onRejected) => promiseThen(promise, onFulfilled, onRejected);
    childCalls.add(state);
    return promise;
  };
  const drainChildren = () => {
    const drain = () => {
      const unsettled = [];
      for (const state of childCalls) if (!state.settled) unsettled.push(state);
      if (unsettled.length) {
        return promiseThen(nativeAll(unsettled.map(state => state.then(undefined, () => undefined))), drain);
      }
      for (const state of childCalls) if (state.failure !== undefined && !state.observed) throw state.failure;
      // Let continuations from the just-settled calls enqueue their own child
      // work before deciding the outer result is complete.
      return promiseThen(nativeResolve(), () => {
        for (const state of childCalls) if (!state.settled) return drain();
        return undefined;
      });
    };
    return drain();
  };
  Object.defineProperty(globalThis, "tools", { value: Object.freeze({
	    search: (query, offset = 0) => {
      const page = search(query, offset);
      const items = page.items;
      Object.defineProperties(items, {
        hasMore: { value: page.hasMore, enumerable: false },
        nextOffset: { value: page.nextOffset, enumerable: false }
      });
      return items;
    },
	    describe,
    invoke: trackedInvoke
  }), writable: false, configurable: false });
  // Console is a bounded diagnostic sink only. It has no reader, filesystem,
  // or process capability, and its entries are never made part of the model
  // transcript or a second observability pipeline.
  Object.defineProperty(globalThis, "console", { value: Object.freeze({
    log: (...values) => log(...values),
    info: (...values) => log(...values),
    warn: (...values) => log(...values),
    error: (...values) => log(...values)
  }), writable: false, configurable: false });
  const AsyncFunction = Object.getPrototypeOf(async function() {}).constructor;
  // A result the bridge cannot represent is a guest error, not a host failure.
  const normalizeResult = value => {
    if (value === undefined) return null;
    const type = typeof value;
    if (type === "function" || type === "symbol" || type === "bigint") {
      throw new TypeError("code must return a JSON-serializable value, received " + type);
    }
    return value;
  };
  // Source arrives as data and is compiled here, so unbalanced brackets cannot
  // close the wrapper and run guest statements before activate() publishes the
  // VM to the watcher. Compilation failures become ordinary JavaScript errors.
  return function(source) {
    activate();
    let user;
    try {
      user = new AsyncFunction(source);
    } catch (error) {
      fail(error);
      return;
    }
    promiseThen(promiseThen(nativeResolve(), user),
      value => promiseThen(drainChildren(), () => complete(normalizeResult(value))),
      fail
    ).catch(fail);
  };
})()`
	if err := e.enterVM(ctx, control, vm, deadline); err != nil {
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
	if err := e.enterVM(ctx, control, vm, deadline); err != nil {
		e.transition(stateCancelRequested)
		e.transition(stateReturned)
		final = runResult{err: err}
		return
	}
	// QuickJS' AsyncFunction constructor assembles its wrapper by concatenation
	// and does not re-parse the body on its own, so an unbalanced source could
	// otherwise close the wrapper and run statements outside the drain/complete
	// protocol. Parse the source once more one block deeper: a source that
	// closes one wrapper always leaves the other unbalanced, while any
	// well-formed function body parses in both. This compiles only — the guest
	// never runs here.
	if _, err := vm.Compile("(async function() {\nif (false) {\n"+source+"\n}\n})", quickjs.EvalGlobal); err != nil {
		e.transition(stateReturned)
		final = runResult{err: fmt.Errorf("javascript execution failed: %s", err)}
		return
	}
	if err := e.enterVM(ctx, control, vm, deadline); err != nil {
		e.transition(stateCancelRequested)
		e.transition(stateReturned)
		final = runResult{err: err}
		return
	}
	if _, err := runner.Call(quickjs.UndefinedValue, source); err != nil {
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
			if fatalErr() != nil {
				e.transition(stateReturned)
				final = runResult{err: fatalExecutionError(fatalErr())}
				return
			}
			e.transition(stateCancelRequested)
			e.transition(stateReturned)
			final = runResult{err: err}
			return
		}
		if fatalErr() != nil && len(pending) == 0 {
			e.transition(stateReturned)
			final = runResult{err: fatalExecutionError(fatalErr())}
			return
		}
		if err := e.enterVM(ctx, control, vm, deadline); err != nil {
			e.transition(stateCancelRequested)
			e.transition(stateReturned)
			final = runResult{err: err}
			return
		}
		jobs, err := vm.ExecutePendingJobs()
		if err != nil {
			if control.cancelRequested.Load() {
				e.transition(stateCancelRequested)
			}
			e.transition(stateReturned)
			final = runResult{err: e.classify(ctx, control, deadline, err)}
			return
		}
		if fatalErr() != nil && len(pending) == 0 {
			e.transition(stateReturned)
			final = runResult{err: fatalExecutionError(fatalErr())}
			return
		}
		if finished.done && len(pending) == 0 {
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
		// Nothing is outstanding and the script has not finished. If its own
		// microtasks are still running, let them; otherwise nothing can ever
		// wake the select below, so the script awaits a promise no one can
		// settle. Only cancellation or the wall clock would end that, and
		// neither tells the model what went wrong.
		if len(pending) == 0 && fatalErr() == nil {
			if jobs > 0 {
				continue
			}
			e.transition(stateReturned)
			final = runResult{err: errors.New("javascript execution failed: the script awaited a promise that can never settle")}
			return
		}

		select {
		case <-ctx.Done():
			accepting = false
			control.observe(ctx)
			if fatalErr() != nil {
				e.transition(stateReturned)
				final = runResult{err: fatalExecutionError(fatalErr())}
				return
			}
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
				if fatalErr() != nil {
					e.transition(stateReturned)
					final = runResult{err: fatalExecutionError(fatalErr())}
					return
				}
				e.transition(stateCancelRequested)
				e.transition(stateReturned)
				final = runResult{err: err}
				return
			}
			if received.fatal != nil {
				delete(pending, received.id)
				releasePromise(promise)
				setFatal(received.fatal)
				continue
			}
			if fatalErr() != nil {
				delete(pending, received.id)
				releasePromise(promise)
				continue
			}
			delete(pending, received.id)
			var settleErr error
			if err := e.enterVM(ctx, control, vm, deadline); err != nil {
				e.transition(stateCancelRequested)
				e.transition(stateReturned)
				final = runResult{err: err}
				return
			}
			if received.err != nil {
				rejection := invocationRejection{Message: received.err.Error(), Code: invocationErrorCode}
				var structured *InvocationError
				if errors.As(received.err, &structured) && json.Valid(structured.Value) {
					if err := json.Unmarshal(structured.Value, &rejection.Value); err != nil {
						settleErr = fmt.Errorf("decode structured host rejection: %w", err)
					}
				}
				if settleErr == nil {
					_, settleErr = promise.capability.Reject.Call(quickjs.UndefinedValue, rejection)
				}
			} else {
				var value any
				if decodeErr := json.Unmarshal(received.result, &value); decodeErr != nil {
					settleErr = fmt.Errorf("decode host completion: %w", decodeErr)
				} else {
					_, settleErr = promise.capability.Resolve.Call(quickjs.UndefinedValue, value)
				}
			}
			releasePromise(promise)
			if settleErr != nil {
				e.transition(stateReturned)
				final = runResult{err: e.classify(ctx, control, deadline, settleErr)}
				return
			}
		}
	}
}

// invokeHost contains a third-party Host panic inside the child boundary. A
// recovered panic is infrastructure failure, never a script-catchable result.
func invokeHost(ctx context.Context, host Host, invocation Invocation) (result json.RawMessage, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("code host invocation panicked")
			result = nil
		}
	}()
	return host.Invoke(ctx, invocation)
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

func (c *runControl) expireDeadline() {
	c.cancelCause.CompareAndSwap(cancelNone, cancelledByDeadline)
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

func (e *Executor) afterInvokeEnqueued() {
	if e.hooks != nil && e.hooks.afterInvokeEnqueued != nil {
		e.hooks.afterInvokeEnqueued()
	}
}

func (e *Executor) onWorkerFatal() {
	if e.hooks != nil && e.hooks.onWorkerFatal != nil {
		e.hooks.onWorkerFatal()
	}
}

func (e *Executor) onOwnerFatal() {
	if e.hooks != nil && e.hooks.onOwnerFatal != nil {
		e.hooks.onOwnerFatal()
	}
}

func catalogArgument(args []any, operation string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("tools.%s requires one string", operation)
	}
	value, ok := args[0].(string)
	if !ok {
		return "", fmt.Errorf("tools.%s requires one string", operation)
	}
	return value, nil
}

func catalogSearchArgument(args []any) (string, int, error) {
	if len(args) != 1 && len(args) != 2 {
		return "", 0, errors.New("tools.search requires a string and optional offset")
	}
	query, ok := args[0].(string)
	if !ok {
		return "", 0, errors.New("tools.search requires a string and optional offset")
	}
	if len(args) == 1 {
		return query, 0, nil
	}
	var offset int
	switch value := args[1].(type) {
	case int:
		offset = value
	case int64:
		offset = int(value)
		if int64(offset) != value {
			return "", 0, errors.New("tools.search offset must be a non-negative integer")
		}
	case float64:
		if value != float64(int(value)) {
			return "", 0, errors.New("tools.search offset must be a non-negative integer")
		}
		offset = int(value)
	default:
		return "", 0, errors.New("tools.search offset must be a non-negative integer")
	}
	if offset < 0 {
		return "", 0, errors.New("tools.search offset must be a non-negative integer")
	}
	return query, offset, nil
}

func cloneCatalog(entries []CatalogEntry) []CatalogEntry {
	out := make([]CatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name == "" {
			continue
		}
		out = append(out, CatalogEntry{
			Name:        entry.Name,
			Description: entry.Description,
			InputSchema: cloneCatalogSchema(entry.InputSchema),
		})
	}
	return out
}

func cloneCatalogSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

type catalogSearchPage struct {
	Items      []map[string]string `json:"items"`
	HasMore    bool                `json:"hasMore"`
	NextOffset int                 `json:"nextOffset"`
}

func searchCatalog(entries []CatalogEntry, query string, offset int) catalogSearchPage {
	query = strings.ToLower(strings.TrimSpace(query))
	results := make([]map[string]string, 0, min(len(entries), catalogResultLimit))
	matched := 0
	more := false
	for _, entry := range entries {
		haystack := strings.ToLower(entry.Name + " " + entry.Description)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		if matched < offset {
			matched++
			continue
		}
		if len(results) == catalogResultLimit {
			more = true
			break
		}
		results = append(results, map[string]string{"name": entry.Name, "description": entry.Description})
		matched++
	}
	return catalogSearchPage{Items: results, HasMore: more, NextOffset: offset + len(results)}
}

func boundedCatalogResponse(value any, limit int) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > limit {
		return ErrPayloadTooLarge
	}
	return nil
}

func describeCatalog(entries []CatalogEntry, name string) (map[string]any, error) {
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		return map[string]any{
			"name":        entry.Name,
			"description": entry.Description,
			"inputSchema": cloneCatalogSchema(entry.InputSchema),
		}, nil
	}
	return nil, fmt.Errorf("tool not found: %s", name)
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

func isExecutionLimit(err error) bool {
	return errors.Is(err, ErrSourceTooLarge) ||
		errors.Is(err, ErrPayloadTooLarge) ||
		errors.Is(err, ErrResultTooLarge) ||
		errors.Is(err, ErrInvocationLimit)
}

func fatalExecutionError(err error) error {
	if isExecutionLimit(err) {
		return err
	}
	return fmt.Errorf("code tool infrastructure failure: %w", err)
}

// enterVM applies the one absolute execution deadline immediately before an
// operation that can run JavaScript. The binding re-arms its per-entry timer,
// so passing the original limit here would otherwise grant every job a fresh
// full budget.
func (e *Executor) enterVM(ctx context.Context, control *runControl, vm *quickjs.VM, deadline time.Time) error {
	control.observe(ctx)
	if err := control.cancellationError(); err != nil {
		return err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		control.expireDeadline()
		return ErrTimedOut
	}
	return vm.SetEvalTimeout(remaining)
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
	// QuickJS emits its deadline interrupt as InternalError. It is a timeout only
	// once the explicit absolute deadline elapsed, never because of its message
	// or merely because a user threw an InternalError.
	var jsErr *quickjs.Error
	if errors.As(err, &jsErr) && jsErr.Name == "InternalError" && !time.Now().Before(deadline) {
		control.expireDeadline()
		return ErrTimedOut
	}
	return err
}
