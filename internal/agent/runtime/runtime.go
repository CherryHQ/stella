package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// Runtime executes agent conversations in already-resolved sessions.
// It owns the runner cache, runner factory, and event streaming.
// It does NOT own session creation, kind validation, or list/archive APIs.
type Runtime struct {
	cache   *runnerCache
	mem     memory.Provider
	log     *slog.Logger
	compact CompactionConfig
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
	Factory      NewRunnerFunc
	Memory       memory.Provider
	IdleTimeout  time.Duration
	Compaction   CompactionConfig
	DefaultModel string
	FastModel    string
	HooksFn      func() []hooks.HookPlugin
}

// New creates a Runtime from the given config.
func New(cfg Config) (*Runtime, error) {
	if cfg.Factory == nil {
		return nil, fmt.Errorf("runtime.Config.Factory is required")
	}
	if cfg.Memory == nil {
		return nil, fmt.Errorf("runtime.Config.Memory is required")
	}
	idleTimeout := cfg.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 10 * time.Minute
	}
	log := slog.With("component", "runtime")
	cache := newRunnerCache(cfg.Factory, cfg.Memory, idleTimeout, log)
	cache.defaultModel = cfg.DefaultModel
	cache.hooksFn = cfg.HooksFn
	return &Runtime{
		cache:   cache,
		mem:     cfg.Memory,
		log:     log,
		compact: cfg.Compaction.WithDefaults(),
	}, nil
}

// SetFactory replaces the runner factory. Existing runners are not affected
// until their session is next reused.
func (rt *Runtime) SetFactory(f NewRunnerFunc) {
	rt.cache.mu.Lock()
	rt.cache.factory = f
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

// Memory returns the memory provider backing this runtime.
func (rt *Runtime) Memory() memory.Provider {
	return rt.mem
}

// Chat executes a user message inside the given session and streams events back.
// info must have been obtained from session.Registry — this method does not
// create or repair session metadata.
func (rt *Runtime) Chat(ctx context.Context, info session.Info, msg MessageContent, opts ...Option) <-chan Event {
	out := make(chan Event, 100)
	var co chatOptions
	for _, o := range opts {
		o(&co)
	}
	ctx = memory.WithSessionID(ctx, info.ID)
	go rt.chat(ctx, out, info, msg, co)
	return out
}
