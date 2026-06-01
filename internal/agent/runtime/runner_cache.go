package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// cachedSession holds one active runner and its metadata.
type cachedSession struct {
	info  session.Info
	r     Runner
	model string
}

// runnerCache manages active runners keyed by session ID.
// It is an implementation detail of Runtime.
type runnerCache struct {
	sessions       map[string]*cachedSession
	factory        NewRunnerFunc
	hooksFn        func() []hooks.HookPlugin
	defaultModel   string
	delegateRunner delegatetool.SessionRunner
	mem            memory.Provider
	idleTimeout    time.Duration
	mu             sync.Mutex
	log            *slog.Logger
}

func newRunnerCache(
	factory NewRunnerFunc,
	mem memory.Provider,
	idleTimeout time.Duration,
	log *slog.Logger,
) *runnerCache {
	return &runnerCache{
		sessions:    make(map[string]*cachedSession),
		factory:     factory,
		mem:         mem,
		idleTimeout: idleTimeout,
		log:         log,
	}
}

// getOrCreate returns an existing runner or creates one.
// info must be fully populated; this method does NOT repair missing fields.
func (c *runnerCache) getOrCreate(ctx context.Context, info session.Info, model string) (*cachedSession, Runner, error) {
	if info.ID == "" {
		return nil, nil, fmt.Errorf("session.Info.ID is required")
	}
	if info.UserID == "" {
		return nil, nil, fmt.Errorf("session.Info.UserID is required")
	}
	if info.AgentID == "" {
		return nil, nil, fmt.Errorf("session.Info.AgentID is required")
	}

	c.mu.Lock()
	cs, ok := c.sessions[info.ID]
	if !ok {
		cs = &cachedSession{info: info}
		c.sessions[info.ID] = cs
	}

	if cs.r != nil {
		switch {
		case !cs.r.Alive():
			c.log.Warn("replacing dead runner", "session_id", info.ID)
			_ = cs.r.Close()
			cs.r = nil
		case model != "" && cs.model != model:
			c.log.Info("switching model", "session_id", info.ID, "from", cs.model, "to", model)
			_ = cs.r.Close()
			cs.r = nil
		default:
			r := cs.r
			c.mu.Unlock()
			return cs, r, nil
		}
	}

	factory := c.factory
	hooksFn := c.hooksFn
	defaultModel := c.defaultModel
	delegateRunner := c.delegateRunner
	c.mu.Unlock()

	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = cs.model
	}
	if effectiveModel == "" {
		effectiveModel = defaultModel
	}

	r, err := factory(ctx, RunnerParams{
		Model:          effectiveModel,
		Memory:         c.mem,
		UserID:         info.UserID,
		SessionID:      info.ID,
		AgentID:        info.AgentID,
		ProjectID:      info.ProjectID,
		HooksFn:        hooksFn,
		DelegateRunner: delegateRunner,
	})
	if err != nil {
		return nil, nil, err
	}

	c.mu.Lock()
	if cs.r != nil {
		// Another goroutine installed a runner; discard ours.
		c.mu.Unlock()
		_ = r.Close()
		return cs, cs.r, nil
	}
	cs.r = r
	cs.model = effectiveModel
	c.mu.Unlock()

	// Bootstrap memory for this session.
	memSess := memory.Session{
		ID:      info.ID,
		AgentID: info.AgentID,
		UserID:  info.UserID,
		Channel: info.Channel,
	}
	if err := c.mem.Bootstrap(ctx, memSess); err != nil {
		c.log.Warn("memory bootstrap failed", "session_id", info.ID, "error", err)
	}

	c.log.Info("created runner", "session_id", info.ID, "model", effectiveModel)
	return cs, r, nil
}

// close shuts down the runner for a single session.
func (c *runnerCache) close(sessionID string) error {
	c.mu.Lock()
	cs, ok := c.sessions[sessionID]
	if ok {
		delete(c.sessions, sessionID)
	}
	c.mu.Unlock()
	if ok && cs.r != nil {
		return cs.r.Close()
	}
	return nil
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
	defer c.mu.Unlock()
	now := time.Now()
	for id, cs := range c.sessions {
		if cs.r == nil || cs.r.Busy() {
			continue
		}
		if !cs.r.Alive() {
			c.log.Warn("removing dead runner", "session_id", id)
			_ = cs.r.Close()
			cs.r = nil
			continue
		}
		if now.Sub(cs.r.LastActivity()) > c.idleTimeout {
			c.log.Info("reaping idle runner",
				"session_id", id,
				"idle_duration", now.Sub(cs.r.LastActivity()).Round(time.Second))
			_ = cs.r.Close()
			cs.r = nil
		}
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
