package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
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
type Runtime struct {
	cache          *runnerCache
	mem            memory.Provider
	log            *slog.Logger
	compact        CompactionConfig
	beforeRun      BeforeRunFunc
	snapshotPrompt SnapshotPromptFunc
	active         sync.Map // session ID → struct{}, tracks in-flight turns
	turns          turnTracker
	hub            *SessionHub
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

// Close shuts down all runners and releases resources.
func (rt *Runtime) Close() error {
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
	rt.cache.mu.Lock()
	var runners []Runner
	for _, cs := range rt.cache.sessions {
		if cs.info.UserID != userID {
			continue
		}
		if cs.r != nil {
			runners = append(runners, cs.r)
			cs.r = nil
		}
		cs.model = ""
	}
	rt.cache.mu.Unlock()

	var lastErr error
	for _, r := range runners {
		if err := r.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
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
func (rt *Runtime) ChatAdmitted(ctx context.Context, info session.Info, msg MessageContent, opts ...Option) (<-chan Event, error) {
	if _, loaded := rt.active.LoadOrStore(info.ID, struct{}{}); loaded {
		return nil, fmt.Errorf("%w: session %s", ErrSessionBusy, info.ID)
	}
	out := make(chan Event, 100)

	var co chatOptions
	for _, o := range opts {
		o(&co)
	}
	ctx = memory.WithSessionID(ctx, info.ID)
	// One identifier per turn. Tools that must know whether a second, real user
	// message arrived since something happened compare turn ids; the runtime
	// admits one turn per session at a time, so "different id" means "different
	// user message" for every turn a user drives.
	ctx = agentctx.WithTurnID(ctx, uuid.Must(uuid.NewV7()).String())

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
	rt.hub.begin(info.ID)
	rt.turns.begin()
	go func() {
		defer rt.turns.end()
		defer rt.active.Delete(info.ID)
		defer func() {
			if p := recover(); p != nil {
				// rt.chat panicked. Close inner so the forwarder drains and
				// rt.hub.end runs; otherwise the session wedges — stuck busy
				// and permanently "live" to SSE watchers. The panic may have
				// unwound through a defer in rt.chat/streamEvents that already
				// closed inner, so tolerate an already-closed channel.
				rt.log.Error("chat turn panicked", "session_id", info.ID, "panic", p)
				safeClose(inner)
			}
		}()
		rt.chat(ctx, inner, info, msg, co)
	}()
	go func() {
		defer close(out)
		defer rt.hub.end(info.ID)
		for ev := range inner {
			rt.hub.publish(info.ID, ev)
			select {
			case out <- ev:
			case <-ctx.Done():
				// Caller gone: keep draining inner so the turn finishes cleanly
				// and hub subscribers still receive, but stop writing to out.
				for ev := range inner {
					rt.hub.publish(info.ID, ev)
				}
				return
			}
		}
	}()
	return out, nil
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
