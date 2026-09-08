package runtime

import (
	"cmp"
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
	info      session.Info
	r         Runner
	model     string
	thinking  ai.ThinkingLevel
	stale     bool
	closing   bool          // Terminal close forbids admission until the entry is removed.
	operation chan struct{} // Creation and Close retain the slot until completion.
	// unusable quarantines a runner after initialization/lookup failure or Close.
	// Only Close may be retried; Alive/Busy may panic or report obsolete state.
	unusable bool
	// reserved is set synchronously during turn admission, before the chat
	// goroutine invokes Runner.Chat. Policy invalidation treats it like Busy so
	// an admitted turn keeps its immutable runner snapshot through that gap.
	reserved bool
}

// runnerSelection is the immutable admission lease for one turn. Cache fields
// remain mutable for the next turn (reset intentionally clears them), so code
// executing an admitted turn must use this value rather than cachedSession.
type runnerSelection struct {
	session        *cachedSession
	runner         Runner
	model          string
	thinking       ai.ThinkingLevel
	beforeRun      BeforeRunFunc
	snapshotPrompt SnapshotPromptFunc
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
	selection, err := c.getOrCreateWithReservation(ctx, info, model, thinking, false, extraTools...)
	if err != nil {
		return nil, nil, err
	}
	return selection.session, selection.runner, nil
}

// getOrCreateReserved atomically assigns the returned runner to an admitted
// turn. It is the only safe admission path: a reaper/reset must never observe
// the selected runner as idle in the hand-off between cache lookup and reserve.
func (c *runnerCache) getOrCreateReserved(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, extraTools ...tools.Tool) (runnerSelection, error) {
	return c.getOrCreateWithReservation(ctx, info, model, thinking, true, extraTools...)
}

// acquire serializes construction and replacement within one cache slot. Other
// sessions keep running, and callers waiting for the slot can cancel admission.
func (c *runnerCache) acquire(ctx context.Context, info session.Info) (*cachedSession, error) {
	for {
		c.mu.Lock()
		cs := c.sessions[info.ID]
		if cs == nil {
			cs = &cachedSession{info: info}
			c.sessions[info.ID] = cs
		}
		if cs.closing {
			c.mu.Unlock()
			return nil, errors.New("runner termination is pending")
		}
		pending := cs.operation
		if pending == nil {
			cs.operation = make(chan struct{})
			c.mu.Unlock()
			return cs, nil
		}
		c.mu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *runnerCache) finishOperation(cs *cachedSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(cs.operation)
	cs.operation = nil
}

func (c *runnerCache) getOrCreateWithReservation(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, reserve bool, extraTools ...tools.Tool) (selection runnerSelection, err error) {
	memSess, err := info.MemoryScope()
	if err != nil {
		return runnerSelection{}, fmt.Errorf("session scope: %w", err)
	}
	cs, err := c.acquire(ctx, info)
	if err != nil {
		return runnerSelection{}, err
	}
	defer c.finishOperation(cs)
	reservationOwned := false
	defer func() {
		panicked := recover() != nil
		if panicked {
			// Recovered values may contain provider credentials.
			c.log.Error("runner construction panicked", "session_id", info.ID)
			selection = runnerSelection{}
			err = errors.New("runner construction failed")
		}
		c.mu.Lock()
		if cs.closing && err == nil {
			selection = runnerSelection{}
			err = errors.New("runner termination is pending")
		}
		if panicked && !cs.reserved {
			cs.unusable, cs.stale = cs.r != nil, true
		}
		c.mu.Unlock()
		if err != nil && reservationOwned {
			c.abortReservedAdmission(cs)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil && cs.r == nil && c.sessions[info.ID] == cs {
			delete(c.sessions, info.ID)
		}
	}()

	var (
		newRunner         NewRunnerFunc
		hooksFn           func() []hooks.HookPlugin
		delegateRunner    delegatetool.SessionRunner
		effectiveModel    string
		effectiveThinking ai.ThinkingLevel
		selected          bool
	)
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		wasReserved := cs.reserved
		reservationOwned = reserve && !wasReserved
		if reserve {
			cs.reserved = true
		}
		if cs.r != nil {
			replace := cs.stale || cs.unusable || len(extraTools) > 0 ||
				(model != "" && cs.model != model) || (thinking != "" && cs.thinking != thinking)
			// A reserved runner belongs to its admitted turn. Never probe it.
			if !wasReserved && !replace {
				replace = !cs.r.Alive()
			}
			if wasReserved || !replace || (!cs.unusable && cs.r.Busy()) {
				cs.stale = replace
				selection = runnerSelection{session: cs, runner: cs.r, model: cs.model, thinking: cs.thinking}
				selected = true
				return
			}
		}
		effectiveModel = cmp.Or(model, cs.model, c.defaultModel)
		effectiveThinking = cmp.Or(thinking, cs.thinking, c.defaultThinking)
		newRunner, hooksFn, delegateRunner = c.newRunner, c.hooksFn, c.delegateRunner
		cs.stale = false
	}()
	if selected {
		return selection, nil
	}
	// The old runner stays discoverable by owner deletion until Close succeeds.
	if err := c.closeRunner(cs, nil); err != nil {
		return runnerSelection{}, err
	}
	c.mu.Lock()
	closing := cs.closing
	c.mu.Unlock()
	if closing {
		return runnerSelection{}, errors.New("runner termination is pending")
	}

	r, err := newRunner(ctx, RunnerParams{
		Model: effectiveModel, Thinking: effectiveThinking, Memory: c.mem,
		UserID: info.UserID, GroupID: info.GroupID, GuestID: info.GuestID,
		ForegroundHuman: foregroundHumanSession(info), SessionID: info.ID,
		AgentID: info.AgentID, ProjectID: info.ProjectID,
		HooksFn: hooksFn, ExtraTools: extraTools, DelegateRunner: delegateRunner,
	})
	if err != nil {
		c.mu.Lock()
		cs.r, cs.unusable = r, r != nil
		c.mu.Unlock()
		return runnerSelection{}, errors.Join(err, c.closeRunner(cs, nil))
	}
	c.mu.Lock()
	cs.r = r
	// Reset can mark an in-flight construction stale without waiting for it.
	// Its admitted turn keeps this snapshot; the following turn rebuilds.
	if !cs.stale {
		cs.model, cs.thinking = effectiveModel, effectiveThinking
	}
	cs.unusable = false
	closing = cs.closing
	c.mu.Unlock()
	if closing {
		return runnerSelection{}, errors.New("runner termination is pending")
	}
	if err := c.mem.Bootstrap(ctx, memSess); err != nil {
		c.log.Warn("memory bootstrap failed", "session_id", info.ID, "error", err)
	}
	c.log.Info("created runner", "session_id", info.ID, "model", effectiveModel)
	return runnerSelection{session: cs, runner: r, model: effectiveModel, thinking: effectiveThinking}, nil
}

// foregroundHumanSession gates discovery of Stella settings tools. It accepts
// only validated direct main/chat sessions, never a worker that happens to use
// the same durable session id.
func foregroundHumanSession(info session.Info) bool {
	return info.UserID != "" &&
		info.GroupID == "" &&
		info.GuestID == "" &&
		(info.Kind == string(session.KindMain) || info.Kind == string(session.KindChat)) &&
		info.Channel != string(session.ChannelWebhook)
}

func (c *runnerCache) reserve(cs *cachedSession) {
	c.mu.Lock()
	if cs != nil && c.sessions[cs.info.ID] == cs {
		cs.reserved = true
	}
	c.mu.Unlock()
}

func (c *runnerCache) releaseReservation(cs *cachedSession) {
	c.mu.Lock()
	if cs != nil && c.sessions[cs.info.ID] == cs {
		cs.reserved = false
	}
	c.mu.Unlock()
}

// abortReservedAdmission unwinds one synchronously admitted turn that never
// reached its chat goroutine. It deliberately leaves an installed runner for
// normal stale replacement rather than closing it under an unknown panic.
func (c *runnerCache) abortReservedAdmission(cs *cachedSession) {
	if cs == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions[cs.info.ID] != cs {
		return
	}
	cs.reserved = false
	if cs.r == nil {
		delete(c.sessions, cs.info.ID)
		return
	}
	cs.stale = true
	cs.unusable = true
	cs.model = ""
	cs.thinking = ""
}

// closeRunner requires ownership of cs.operation. A failed or panicking Close
// retains the same runner for retry, including during destructive owner cleanup.
func (c *runnerCache) closeRunner(cs *cachedSession, cb SandboxSessionCallback) (err error) {
	defer func() {
		if recover() != nil {
			c.log.Error("runner close panicked")
			err = errors.New("runner close failed")
		}
	}()
	c.mu.Lock()
	r := cs.r
	cs.unusable = r != nil
	c.mu.Unlock()
	var cbErr error
	if r != nil {
		if cb != nil {
			if sr, ok := r.(interface{ SandboxSession() pkgsandbox.Session }); ok {
				if sess := sr.SandboxSession(); sess != nil {
					cbErr = cb(sess)
				}
			}
		}
		if err := r.Close(); err != nil {
			return errors.Join(cbErr, err)
		}
	}
	c.mu.Lock()
	cs.r = nil
	cs.unusable = false
	c.mu.Unlock()
	return cbErr
}

func (c *runnerCache) close(sessionID string) error {
	return c.closeWithSandbox(sessionID, nil)
}

// closeWithSandbox invokes cb just before Close. Failed termination retains the
// slot, so a retry cannot overlap an older execution generation.
func (c *runnerCache) closeWithSandbox(sessionID string, cb SandboxSessionCallback) error {
	return c.terminalClose(func(cs *cachedSession) bool { return cs.info.ID == sessionID }, cb)
}

func (c *runnerCache) reset() error { return c.resetWhere(nil) }

func (c *runnerCache) resetWhere(include func(*cachedSession) bool) error {
	return c.retireIdle(func(cs *cachedSession) bool {
		if include != nil && !include(cs) {
			return false
		}
		cs.model, cs.thinking = "", ""
		return true
	})
}

func (c *runnerCache) invalidateSkillPolicy() error {
	return c.retireIdle(func(*cachedSession) bool { return true })
}

// retireIdle preserves admitted work and never waits for a factory. It claims
// idle slots under c.mu so admission cannot slip between selection and Close.
func (c *runnerCache) retireIdle(include func(*cachedSession) bool) error {
	var closing []*cachedSession
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, cs := range c.sessions {
			if cs.closing || !include(cs) {
				continue
			}
			cs.stale = true
			if cs.reserved || cs.operation != nil || (cs.r != nil && !cs.unusable && cs.r.Busy()) {
				continue
			}
			cs.operation = make(chan struct{})
			closing = append(closing, cs)
		}
	}()
	var errs []error
	for _, cs := range closing {
		errs = append(errs, c.closeRunner(cs, nil))
		c.finishOperation(cs)
	}
	return errors.Join(errs...)
}

func (c *runnerCache) closeAll() error {
	return c.closeWhere(func(*cachedSession) bool { return true })
}

func (c *runnerCache) closeWhere(include func(*cachedSession) bool) error {
	return c.terminalClose(include, nil)
}

// Terminal close first blocks admission for every matching slot, then waits for
// any in-flight construction/Close. Owner deletion cannot overlook a factory
// that has not returned its runner yet. Unlike reset, it may interrupt Busy work.
func (c *runnerCache) terminalClose(include func(*cachedSession) bool, cb SandboxSessionCallback) error {
	c.mu.Lock()
	var closing []*cachedSession
	for _, cs := range c.sessions {
		if include(cs) {
			cs.closing = true
			closing = append(closing, cs)
		}
	}
	c.mu.Unlock()
	var errs []error
	for _, cs := range closing {
		c.mu.Lock()
		for cs.operation != nil {
			pending := cs.operation
			c.mu.Unlock()
			<-pending
			c.mu.Lock()
		}
		if c.sessions[cs.info.ID] != cs {
			c.mu.Unlock()
			continue
		}
		cs.operation = make(chan struct{})
		c.mu.Unlock()
		err := c.closeRunner(cs, cb)
		c.mu.Lock()
		if cs.r == nil {
			delete(c.sessions, cs.info.ID)
		}
		c.mu.Unlock()
		c.finishOperation(cs)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c *runnerCache) reap() {
	_ = c.retireIdle(func(cs *cachedSession) bool {
		if cs.reserved || cs.operation != nil || cs.r == nil {
			return false
		}
		return cs.unusable || (!cs.r.Busy() && (!cs.r.Alive() || time.Since(cs.r.LastActivity()) > c.idleTimeout))
	})
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
