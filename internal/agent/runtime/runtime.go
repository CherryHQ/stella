package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
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
}

// CompactionConfig controls automatic compaction thresholds.
type CompactionConfig struct {
	MaxTokens int
	KeepTail  int
}

// WithDefaults returns the config with zero values replaced by defaults.
func (c CompactionConfig) WithDefaults() CompactionConfig {
	if c.MaxTokens == 0 {
		c.MaxTokens = 80_000
	}
	if c.KeepTail == 0 {
		c.KeepTail = 20
	}
	return c
}

// Config holds all dependencies for a Runtime instance.
type Config struct {
	NewRunner      NewRunnerFunc
	Memory         memory.Provider
	IdleTimeout    time.Duration
	Compaction     CompactionConfig
	DefaultModel   string
	FastModel      string
	HooksFn        func() []hooks.HookPlugin
	BeforeRun      BeforeRunFunc
	SnapshotPrompt SnapshotPromptFunc
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
	cache.hooksFn = cfg.HooksFn
	return &Runtime{
		cache:          cache,
		mem:            cfg.Memory,
		log:            log,
		compact:        cfg.Compaction.WithDefaults(),
		beforeRun:      cfg.BeforeRun,
		snapshotPrompt: cfg.SnapshotPrompt,
	}, nil
}

// SetNewRunner replaces the runner builder. Existing runners are not affected
// until their session is next reused.
func (rt *Runtime) SetNewRunner(f NewRunnerFunc) {
	rt.cache.mu.Lock()
	rt.cache.newRunner = f
	rt.cache.mu.Unlock()
}

// SetDefaultModel updates the default model for new runners.
func (rt *Runtime) SetDefaultModel(model string) {
	rt.cache.mu.Lock()
	rt.cache.defaultModel = model
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
func (rt *Runtime) Chat(ctx context.Context, info session.Info, msg MessageContent, opts ...Option) <-chan Event {
	out := make(chan Event, 100)

	if _, loaded := rt.active.LoadOrStore(info.ID, struct{}{}); loaded {
		out <- Event{Err: fmt.Errorf("%w: session %s", ErrSessionBusy, info.ID)}
		close(out)
		return out
	}

	var co chatOptions
	for _, o := range opts {
		o(&co)
	}
	ctx = memory.WithSessionID(ctx, info.ID)
	go func() {
		defer rt.active.Delete(info.ID)
		rt.chat(ctx, out, info, msg, co)
	}()
	return out
}
