package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// Runtime executes agent conversations in already-resolved sessions.
// It owns the runner cache, runner factory, and event streaming.
// It does NOT own session creation, kind validation, or list/archive APIs.
// SessionImages converts raw image blocks into canonical references at the
// ordinary-session write chokepoint. The full pipeline also hydrates active
// media for runners; Runtime only needs its enrichment operation.
type SessionImages interface {
	Enrich(context.Context, string, string, []ai.ContentBlock) ([]ai.ContentBlock, error)
}

type Runtime struct {
	cache          *runnerCache
	mem            memory.Provider
	log            *slog.Logger
	compact        CompactionConfig
	promptMu       sync.RWMutex
	beforeRun      BeforeRunFunc
	snapshotPrompt SnapshotPromptFunc
	sessionImages  SessionImages
	active         sync.Map // session ID → *activeTurn, tracks in-flight turns
	turns          turnTracker
	hub            *SessionHub
	closed         atomic.Bool
}

// managedSessionRunner is intentionally optional: Runtime supports test and
// specialized Runner implementations that do not carry the delegate tool. Only
// the production agent runner can bridge Session create/send to that tool.
type managedSessionRunner interface {
	RunManagedSession(context.Context, delegatetool.ManagedSessionRequest) (delegatetool.ManagedSessionResult, error)
}

// turnTracker counts in-flight chat turns so a graceful drain can wait,
// bounded, for accepted work to finish before teardown cancels its
// dependencies (#744). It is not a sync.WaitGroup because a turn may still
// begin while the drain is already waiting (a keep-alive HTTP connection can
// start one mid-drain), which WaitGroup's Add/Wait contract forbids; the
// tracker instead loops until it observes zero.
type turnTracker struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed when n drops to 0; replaced when n rises from 0
}

func (t *turnTracker) begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n == 0 {
		t.idle = make(chan struct{})
	}
	t.n++
}

func (t *turnTracker) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n--
	if t.n == 0 {
		close(t.idle)
	}
}

// wait blocks until no turn is in flight or ctx expires. A nil error means an
// idle instant was observed; work accepted after that races the caller's next
// step, which is why the drain stops ingress before it waits.
func (t *turnTracker) wait(ctx context.Context) error {
	for {
		t.mu.Lock()
		if t.n == 0 {
			t.mu.Unlock()
			return nil
		}
		idle := t.idle
		t.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// WaitTurns blocks until this runtime has no in-flight chat turn or ctx
// expires. Graceful shutdown calls it between draining HTTP and cancelling the
// work contexts, so turns with no HTTP connection to hold the drain open
// (channel messages, webhook runs, scheduler run-now) still finish. It covers
// the turn itself, not the caller's sub-second delivery tail after the event
// stream closes; lift tracking to the adapter operation if truncated final
// sends are ever observed.
func (rt *Runtime) WaitTurns(ctx context.Context) error {
	return rt.turns.wait(ctx)
}

// InvalidateSkillPolicy schedules existing runners for refresh without
// interrupting an in-flight turn, which continues with its copied snapshot.
func (rt *Runtime) InvalidateSkillPolicy() error { return rt.cache.invalidateSkillPolicy() }

// CompactionConfig controls automatic compaction thresholds.
type CompactionConfig struct {
	MaxTokens int
	// KeepTail is the number of recent user turns preserved verbatim.
	KeepTail int
}

// WithDefaults returns the config with zero values replaced by defaults.
func (c CompactionConfig) WithDefaults() CompactionConfig {
	if c.MaxTokens == 0 {
		c.MaxTokens = 80_000
	}
	if c.KeepTail == 0 {
		c.KeepTail = 6
	}
	return c
}

// Config holds all dependencies for a Runtime instance.
type Config struct {
	NewRunner       NewRunnerFunc
	Memory          memory.Provider
	IdleTimeout     time.Duration
	Compaction      CompactionConfig
	DefaultModel    string
	DefaultThinking ai.ThinkingLevel
	FastModel       string
	HooksFn         func() []hooks.HookPlugin
	BeforeRun       BeforeRunFunc
	SnapshotPrompt  SnapshotPromptFunc
	SessionImages   SessionImages
}

// New creates a Runtime from the given config.
func New(cfg Config) (*Runtime, error) {
	if cfg.NewRunner == nil {
		return nil, fmt.Errorf("runtime.Config.NewRunner is required")
	}
	if cfg.Memory == nil {
		return nil, fmt.Errorf("runtime.Config.Memory is required")
	}
	idleTimeout := cfg.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 10 * time.Minute
	}
	log := slog.With("component", "runtime")
	cache := newRunnerCache(cfg.NewRunner, cfg.Memory, idleTimeout, log)
	cache.defaultModel = cfg.DefaultModel
	cache.defaultThinking = cfg.DefaultThinking
	cache.hooksFn = cfg.HooksFn
	return &Runtime{
		cache:          cache,
		mem:            cfg.Memory,
		log:            log,
		compact:        cfg.Compaction.WithDefaults(),
		beforeRun:      cfg.BeforeRun,
		snapshotPrompt: cfg.SnapshotPrompt,
		sessionImages:  cfg.SessionImages,
		hub:            NewSessionHub(),
	}, nil
}

// Subscribe registers a read-only listener for a session's live turn events.
// The channel is closed when the in-flight turn ends; callers must invoke the
// returned cancel func when they stop reading. See SessionHub.
func (rt *Runtime) Subscribe(sessionID string) (<-chan Event, func()) {
	return rt.hub.Subscribe(sessionID)
}

// SessionLive reports whether a turn is currently in flight on the session.
func (rt *Runtime) SessionLive(sessionID string) bool {
	return rt.hub.IsLive(sessionID)
}

// RunManagedSession executes a Session-tool request through the currently
// active source runner. The source runner owns the effective delegate preset,
// system override, timeout, and excluded-tool set for this turn.
func (rt *Runtime) RunManagedSession(ctx context.Context, sourceSessionID string, req delegatetool.ManagedSessionRequest) (delegatetool.ManagedSessionResult, error) {
	if sourceSessionID == "" {
		return delegatetool.ManagedSessionResult{}, fmt.Errorf("managed session source is unavailable")
	}
	rt.cache.mu.Lock()
	cs := rt.cache.sessions[sourceSessionID]
	var runner Runner
	if cs != nil {
		runner = cs.r
	}
	rt.cache.mu.Unlock()
	managed, ok := runner.(managedSessionRunner)
	if !ok {
		return delegatetool.ManagedSessionResult{}, fmt.Errorf("managed session runner is unavailable")
	}
	return managed.RunManagedSession(ctx, req)
}

// SetNewRunner replaces the runner builder. Existing runners are not affected
// until their session is next reused.
func (rt *Runtime) SetNewRunner(f NewRunnerFunc) {
	rt.cache.mu.Lock()
	rt.cache.newRunner = f
	rt.cache.mu.Unlock()
}

// SetDefaultModel updates the default model for new runners.
func (rt *Runtime) SetDefaultModel(model string, thinking ai.ThinkingLevel) {
	rt.cache.mu.Lock()
	rt.cache.defaultModel = model
	rt.cache.defaultThinking = thinking
	rt.cache.mu.Unlock()
}

// SetPromptBuilders replaces the snapshot-derived prompt builders for turns
// admitted after a configuration refresh. Callers serialize this with runner
// replacement through the Agent admission barrier.
func (rt *Runtime) SetPromptBuilders(beforeRun BeforeRunFunc, snapshotPrompt SnapshotPromptFunc) {
	rt.promptMu.Lock()
	rt.beforeRun = beforeRun
	rt.snapshotPrompt = snapshotPrompt
	rt.promptMu.Unlock()
}

func (rt *Runtime) capturePromptBuilders(selection *runnerSelection) {
	rt.promptMu.RLock()
	selection.beforeRun = rt.beforeRun
	selection.snapshotPrompt = rt.snapshotPrompt
	rt.promptMu.RUnlock()
}

// SetHooks updates the hook getter used when creating new runners.
func (rt *Runtime) SetHooks(fn func() []hooks.HookPlugin) {
	rt.cache.mu.Lock()
	rt.cache.hooksFn = fn
	rt.cache.mu.Unlock()
}

// SetDelegateRunner wires the delegate session runner into new runners.
// Call after construction so runners can spawn persistent child sessions.
func (rt *Runtime) SetDelegateRunner(r delegatetool.SessionRunner) {
	rt.cache.mu.Lock()
	rt.cache.delegateRunner = r
	rt.cache.mu.Unlock()
}

// CloseSession closes the runner for a single session without affecting others.
func (rt *Runtime) CloseSession(_ context.Context, sessionID string) error {
	return rt.cache.close(sessionID)
}

// Close shuts down all runners and rejects every later admission.
func (rt *Runtime) Close() error {
	rt.closed.Store(true)
	return rt.cache.closeAll()
}

// StartReaper begins the idle-runner eviction loop. Call in a goroutine.
func (rt *Runtime) StartReaper(ctx context.Context) {
	rt.cache.StartReaper(ctx)
}

// ResetRunners closes all live runners while keeping session metadata in storage.
func (rt *Runtime) ResetRunners() error {
	return rt.cache.reset()
}

// ResetRunnersForUser closes live runners for a specific user.
func (rt *Runtime) ResetRunnersForUser(userID string) error {
	return rt.cache.resetWhere(func(cs *cachedSession) bool { return cs.info.UserID == userID })
}

// NewRunnerFunc returns the current runner builder. Used by the task system to
// create standalone runners with custom tools.
func (rt *Runtime) NewRunnerFunc() NewRunnerFunc {
	rt.cache.mu.Lock()
	f := rt.cache.newRunner
	rt.cache.mu.Unlock()
	return f
}

// Memory returns the memory provider backing this runtime.
func (rt *Runtime) Memory() memory.Provider {
	return rt.mem
}

// ErrSessionBusy is returned when a session already has an active chat turn.
var ErrSessionBusy = agenterr.ErrSessionBusy

// Chat executes a user message inside the given session and streams events back.
// info must have been obtained from session.Registry — this method does not
// create or repair session metadata.
//
// Only one active turn per session is allowed. A second concurrent Chat on the
// same session returns ErrSessionBusy immediately.
// safeClose closes ch, tolerating an already-closed channel. The panic-recovery
// path in Chat cannot know whether rt.chat closed inner before unwinding.
func safeClose(ch chan Event) {
	defer func() { _ = recover() }()
	close(ch)
}

// ChatAdmitted starts one turn only after synchronously acquiring the session's
// busy guard. A nil error means the turn is admitted; every later runtime failure
// is delivered on the returned stream. ErrSessionBusy means no turn was started,
// so the caller can decide before any run/session/tool side effect is visible.
type activeTurn struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (rt *Runtime) ChatAdmitted(ctx context.Context, info session.Info, msg MessageContent, opts ...Option) (<-chan Event, error) {
	return rt.ChatAdmittedControlled(ctx, info, msg, nil, opts...)
}

// ChatAdmittedControlled is ChatAdmitted with a final admission fence. The
// fence runs after the busy guard is acquired but before transcript/runtime
// side effects, allowing a synchronous queue to reject a caller that timed out
// at the admission boundary without replay or ambiguous execution.
func (rt *Runtime) ChatAdmittedControlled(ctx context.Context, info session.Info, msg MessageContent, beforeStart func() error, opts ...Option) (stream <-chan Event, admissionErr error) {
	activityScope, err := info.MemoryScope()
	if err != nil {
		return nil, err
	}
	var (
		selection      runnerSelection
		selectionReady bool
		turn           *activeTurn
	)
	defer func() {
		if recover() == nil {
			return
		}
		// This is only the narrow synchronous admission envelope. Deep cache
		// construction owns its own cleanup; this protects options/UUID/hub setup
		// and any post-selection panic before the goroutine's async recovery.
		// Do not log recovered values: provider panics may contain secrets.
		rt.log.Error("chat admission panicked", "session_id", info.ID)
		if selectionReady {
			rt.cache.abortReservedAdmission(selection.session)
		}
		if turn != nil {
			turn.cancel()
			if rt.active.CompareAndDelete(info.ID, turn) {
				close(turn.done)
			}
		}
		stream = nil
		admissionErr = errors.New("chat admission failed")
	}()
	if rt.closed.Load() {
		return nil, errors.New("runtime is closed")
	}
	turnCtx, cancel := context.WithCancel(ctx)
	turn = &activeTurn{cancel: cancel, done: make(chan struct{})}
	if _, loaded := rt.active.LoadOrStore(info.ID, turn); loaded {
		cancel()
		return nil, fmt.Errorf("%w: session %s", ErrSessionBusy, info.ID)
	}
	if err := turnCtx.Err(); err != nil {
		rt.active.CompareAndDelete(info.ID, turn)
		cancel()
		return nil, err
	}
	if beforeStart != nil {
		if err := beforeStart(); err != nil {
			rt.active.CompareAndDelete(info.ID, turn)
			cancel()
			return nil, err
		}
	}
	out := make(chan Event, 100)

	var co chatOptions
	for _, o := range opts {
		o(&co)
	}
	ctx = memory.WithSessionID(turnCtx, info.ID)
	// One identifier per turn. Tools that must know whether a second, real user
	// message arrived since something happened compare turn ids; the runtime
	// admits one turn per session at a time, so "different id" means "different
	// user message" for every turn a user drives.
	ctx = agentctx.WithTurnID(ctx, uuid.Must(uuid.NewV7()).String())
	ctx = withSessionIdentity(ctx, info)
	// Select and reserve the runner before returning admission. Service holds its
	// per-Agent policy barrier around this call, so a policy commit cannot swap
	// factories or stale an idle old runner between active registration and
	// runner selection. The reservation protects the gap before Runner.Chat
	// marks itself busy in the goroutine below.
	selection, err = rt.getOrCreateReservedRunner(ctx, info, co.model, co.extraTools)
	if err != nil {
		cancel()
		if rt.active.CompareAndDelete(info.ID, turn) {
			close(turn.done)
		}
		return nil, fmt.Errorf("get runner: %w", err)
	}
	selectionReady = true
	rt.capturePromptBuilders(&selection)
	rt.markSessionTurnStarted(ctx, activityScope)

	// Tee: chat writes to inner; the forwarder fans every event out to the hub
	// (read-only subscribers — SSE watchers of scheduler/task/delegate turns, or
	// another tab) as well as to the caller's channel.
	//
	// A server-driven turn (scheduler/task) runs under its own context, so
	// watchers can attach and detach without affecting it. A user-initiated turn
	// runs under the caller's request context and ends when that caller
	// disconnects; the ctx.Done branch below only flushes already-buffered
	// events rather than blocking on a reader that is gone.
	inner := make(chan Event, 100)
	producerResult := make(chan memory.SessionTurnResult, 1)
	rt.hub.begin(info.ID)
	rt.turns.begin()
	go func() {
		result := memory.SessionTurnSuccess
		defer rt.turns.end()
		defer func() {
			if p := recover(); p != nil {
				// rt.chat panicked. Close inner so the forwarder drains and
				// rt.hub.end runs; otherwise the session wedges — stuck busy
				// and permanently "live" to SSE watchers. The panic may have
				// unwound through a defer in rt.chat/streamEvents that already
				// closed inner, so tolerate an already-closed channel.
				rt.log.Error("chat turn panicked", "session_id", info.ID, "panic", p)
				result = memory.SessionTurnError
				safeClose(inner)
			}
			if result != memory.SessionTurnError && ctx.Err() != nil {
				result = memory.SessionTurnCanceled
			}
			producerResult <- result
		}()
		rt.chatWithRunner(ctx, inner, info, msg, co, selection)
	}()
	go func() {
		defer close(out)
		defer close(turn.done)
		defer cancel()
		defer rt.active.CompareAndDelete(info.ID, turn)
		defer rt.hub.end(info.ID)
		result := memory.SessionTurnSuccess
		deliver := true
		for ev := range inner {
			rt.hub.publish(info.ID, ev)
			if ev.Err != nil {
				result = memory.SessionTurnError
			}
			if !deliver {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				// Caller gone: keep draining inner so the turn finishes cleanly and
				// hub subscribers still receive, but stop writing to out.
				deliver = false
			}
		}
		producerOutcome := <-producerResult
		if result != memory.SessionTurnError {
			result = producerOutcome
		}
		// Completion is durable before hub/out observers see EOF and mark the
		// session viewed.
		rt.markSessionTurnCompleted(ctx, activityScope, result)
	}()
	return out, nil
}

func (rt *Runtime) markSessionTurnStarted(ctx context.Context, session memory.Session) {
	activity, ok := rt.mem.(memory.SessionActivityStore)
	if !ok {
		return
	}
	if _, err := activity.MarkSessionTurnStarted(context.WithoutCancel(ctx), session); err != nil {
		rt.log.Warn("mark session turn started failed", "session_id", session.ID, "error", err)
	}
}

func (rt *Runtime) markSessionTurnCompleted(ctx context.Context, session memory.Session, result memory.SessionTurnResult) {
	activity, ok := rt.mem.(memory.SessionActivityStore)
	if !ok {
		return
	}
	if _, err := activity.MarkSessionTurnCompleted(context.WithoutCancel(ctx), session, result); err != nil {
		rt.log.Warn("mark session turn completed failed", "session_id", session.ID, "error", err)
	}
}

// stopWaitCeiling keeps a broken provider from pinning the stop HTTP request.
// Cooperative providers finish immediately; increase only if a real backend
// needs a longer cancellation unwind.
const stopWaitCeiling = 5 * time.Second

// StopSession cancels the active turn for sessionID. It returns false when the
// session has no in-flight turn. Disconnecting an observer never calls this;
// cancellation is an explicit, authorized action at the Session boundary.
func (rt *Runtime) StopSession(ctx context.Context, sessionID string) bool {
	value, ok := rt.active.Load(sessionID)
	if !ok {
		return false
	}
	turn, ok := value.(*activeTurn)
	if !ok {
		return false
	}
	turn.cancel()
	timer := time.NewTimer(stopWaitCeiling)
	defer timer.Stop()
	select {
	case <-turn.done:
	case <-ctx.Done():
	case <-timer.C:
	}
	return true
}

// Chat preserves the historic stream-only API. A rejected admission surfaces as
// the historic immediate error event; callers that must distinguish a rejected
// admission from an admitted turn (e.g. webhook ingress) use ChatAdmitted.
func (rt *Runtime) Chat(ctx context.Context, info session.Info, msg MessageContent, opts ...Option) <-chan Event {
	stream, err := rt.ChatAdmitted(ctx, info, msg, opts...)
	if err != nil {
		return errorStream(err)
	}
	return stream
}

func errorStream(err error) <-chan Event {
	out := make(chan Event, 1)
	out <- Event{Err: err}
	close(out)
	return out
}
