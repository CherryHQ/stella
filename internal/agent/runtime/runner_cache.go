package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

// cachedSession holds one active runner and its metadata.
type cachedSession struct {
	info     session.Info
	r        Runner
	model    string
	thinking ai.ThinkingLevel
	// privateHuman is part of the runner cache identity because it controls the
	// registered tools and the base system prompt.
	privateHuman bool
}

// runnerCache manages active runners keyed by session ID.
// It is an implementation detail of Runtime.
type runnerCache struct {
	sessions        map[string]*cachedSession
	newRunner       NewRunnerFunc
	hooksFn         func() []hooks.HookPlugin
	defaultModel    string
	defaultThinking ai.ThinkingLevel
	delegateRunner  delegatetool.SessionRunner
	mem             memory.Provider
	idleTimeout     time.Duration
	mu              sync.Mutex
	log             *slog.Logger
}

func newRunnerCache(
	newRunner NewRunnerFunc,
	mem memory.Provider,
	idleTimeout time.Duration,
	log *slog.Logger,
) *runnerCache {
	return &runnerCache{
		sessions:    make(map[string]*cachedSession),
		newRunner:   newRunner,
		mem:         mem,
		idleTimeout: idleTimeout,
		log:         log,
	}
}

// getOrCreate returns an existing runner or creates one.
// info must be fully populated; this method does NOT repair missing fields.
// Passing extraTools always builds a fresh runner (per-call tools defeat the
// cache); the caller is expected to evict it afterwards via CloseSession.
func (c *runnerCache) getOrCreate(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, extraTools ...tools.Tool) (*cachedSession, Runner, error) {
	// Validate the session and derive its memory scope before any cache lookup or
	// runner creation. An invalid session (missing owner, malformed group id) must
	// fail closed here so a runner is never installed over an unusable scope.
	memSess, err := info.MemoryScope()
	if err != nil {
		return nil, nil, fmt.Errorf("session scope: %w", err)
	}

	privateHuman := privateHumanTurnFromContext(ctx)

	c.mu.Lock()
	cs, ok := c.sessions[info.ID]
	if !ok {
		cs = &cachedSession{info: info}
		c.sessions[info.ID] = cs
	}

	var stale Runner
	if cs.r != nil {
		switch {
		case len(extraTools) > 0:
			stale = cs.r
			cs.r = nil
		case !cs.r.Alive():
			c.log.Warn("replacing dead runner", "session_id", info.ID)
			stale = cs.r
			cs.r = nil
		case model != "" && cs.model != model:
			c.log.Info("switching model", "session_id", info.ID, "from", cs.model, "to", model)
			stale = cs.r
			cs.r = nil
		case thinking != "" && cs.thinking != thinking:
			c.log.Info("switching thinking level", "session_id", info.ID, "from", cs.thinking, "to", thinking)
			stale = cs.r
			cs.r = nil
		case cs.privateHuman != privateHuman:
			// A durable session can be entered from different surfaces. Rebuild
			// so a runner created for a human turn is never reused by a webhook.
			stale = cs.r
			cs.r = nil
		default:
			r := cs.r
			c.mu.Unlock()
			return cs, r, nil
		}
	}

	newRunner := c.newRunner
	hooksFn := c.hooksFn
	defaultModel := c.defaultModel
	defaultThinking := c.defaultThinking
	delegateRunner := c.delegateRunner
	cachedModel := cs.model
	cachedThinking := cs.thinking
	c.mu.Unlock()

	if stale != nil {
		_ = stale.Close()
	}

	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = cachedModel
	}
	if effectiveModel == "" {
		effectiveModel = defaultModel
	}
	effectiveThinking := thinking
	if effectiveThinking == "" {
		effectiveThinking = cachedThinking
	}
	if effectiveThinking == "" {
		effectiveThinking = defaultThinking
	}

	r, err := newRunner(ctx, RunnerParams{
		Model:          effectiveModel,
		Thinking:       effectiveThinking,
		Memory:         c.mem,
		UserID:         info.UserID,
		GroupID:        info.GroupID,
		SessionID:      info.ID,
		AgentID:        info.AgentID,
		ProjectID:      info.ProjectID,
		SessionKind:    info.Kind,
		SessionChannel: info.Channel,
		PrivateHuman:   privateHuman,
		HooksFn:        hooksFn,
		ExtraTools:     extraTools,
		DelegateRunner: delegateRunner,
	})
	if err != nil {
		c.mu.Lock()
		if current := c.sessions[info.ID]; current == cs && cs.r == nil {
			delete(c.sessions, info.ID)
		}
		c.mu.Unlock()
		return nil, nil, err
	}

	c.mu.Lock()
	if cs.r != nil {
		// Another goroutine installed a runner; discard ours.
		existing := cs.r
		c.mu.Unlock()
		_ = r.Close()
		return cs, existing, nil
	}
	cs.r = r
	cs.model = effectiveModel
	cs.thinking = effectiveThinking
	cs.privateHuman = privateHuman
	c.mu.Unlock()

	// Bootstrap memory for this session using the scope derived up front.
	if err := c.mem.Bootstrap(ctx, memSess); err != nil {
		c.log.Warn("memory bootstrap failed", "session_id", info.ID, "error", err)
	}

	c.log.Info("created runner", "session_id", info.ID, "model", effectiveModel)
	return cs, r, nil
}

// close shuts down the runner for a single session.
func (c *runnerCache) close(sessionID string) error {
	return c.closeWithSandbox(sessionID, nil)
}

// closeWithSandbox invokes cb with the live runner-owned sandbox, when present,
// immediately before closing the runner. Close still runs if cb fails.
func (c *runnerCache) closeWithSandbox(sessionID string, cb SandboxSessionCallback) error {
	c.mu.Lock()
	cs, ok := c.sessions[sessionID]
	if ok {
		delete(c.sessions, sessionID)
	}
	c.mu.Unlock()
	if !ok || cs.r == nil {
		return nil
	}

	var cbErr error
	if cb != nil {
		if sr, ok := cs.r.(interface{ SandboxSession() pkgsandbox.Session }); ok {
			if sess := sr.SandboxSession(); sess != nil {
				cbErr = cb(sess)
			}
		}
	}
	return errors.Join(cbErr, cs.r.Close())
}

func (c *runnerCache) reset() error {
	c.mu.Lock()
	runners := make([]Runner, 0, len(c.sessions))
	for _, cs := range c.sessions {
		if cs.r != nil {
			runners = append(runners, cs.r)
			cs.r = nil
		}
		cs.model = ""
		cs.thinking = ""
	}
	c.mu.Unlock()

	var lastErr error
	for _, r := range runners {
		if err := r.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// closeAll shuts down all runners.
func (c *runnerCache) closeAll() error {
	c.mu.Lock()
	sessions := c.sessions
	c.sessions = make(map[string]*cachedSession)
	c.mu.Unlock()

	var lastErr error
	for _, cs := range sessions {
		if cs.r != nil {
			if err := cs.r.Close(); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

// reap closes runners that are idle or dead.
func (c *runnerCache) reap() {
	c.mu.Lock()
	now := time.Now()
	var closing []Runner
	for id, cs := range c.sessions {
		if cs.r == nil || cs.r.Busy() {
			continue
		}
		lastActivity := cs.r.LastActivity()
		if !cs.r.Alive() {
			c.log.Warn("removing dead runner", "session_id", id)
			closing = append(closing, cs.r)
			cs.r = nil
			continue
		}
		if now.Sub(lastActivity) > c.idleTimeout {
			c.log.Info("reaping idle runner",
				"session_id", id,
				"idle_duration", now.Sub(lastActivity).Round(time.Second))
			closing = append(closing, cs.r)
			cs.r = nil
		}
	}
	c.mu.Unlock()

	for _, r := range closing {
		_ = r.Close()
	}
}

// StartReaper runs a background goroutine that periodically reaps runners.
func (c *runnerCache) StartReaper(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reap()
		}
	}
}
