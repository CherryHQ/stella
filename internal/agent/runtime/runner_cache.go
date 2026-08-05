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
	stale    bool
	// failedAdmission marks a stale runner left behind by a recovered synchronous
	// lookup panic. It must be retired without invoking it again: Alive/Busy may
	// be the operation that panicked.
	failedAdmission bool
	// leases counts the outstanding use holds on this runner. A turn admission
	// takes one from selection until releaseReservation; a Filesystem callback
	// takes one from acquireFilesystemUse until releaseFilesystemUse. Each is
	// acquired and released exactly once by its owner, and the two coexist. A
	// non-zero count is authoritative: policy invalidation, reset, and the reaper
	// treat the runner like Busy so an in-flight hold keeps its runner snapshot.
	leases int
}

// runnerSelection is the immutable admission lease for one turn. Cache fields
// remain mutable for the next turn (reset intentionally clears them), so code
// executing an admitted turn must use this value rather than cachedSession.
type runnerSelection struct {
	session  *cachedSession
	runner   Runner
	model    string
	thinking ai.ThinkingLevel
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

func (c *runnerCache) getOrCreateWithReservation(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, reserve bool, extraTools ...tools.Tool) (selection runnerSelection, err error) {
	var (
		cs               *cachedSession
		created          bool
		reservationOwned bool
	)
	defer func() {
		if recover() == nil {
			return
		}
		// Construction runs synchronously after Runtime has installed its busy
		// guard. Recover here, at the boundary that owns the reservation, so a
		// malformed provider/memory implementation cannot wedge that session.
		// Never include recovered values: panics can contain provider secrets.
		c.log.Error("runner construction panicked", "session_id", info.ID)
		c.recoverFailedAdmission(cs, created, reservationOwned)
		selection = runnerSelection{}
		err = errors.New("runner construction failed")
	}()
	// acquire takes this invocation's single lease and records ownership in the
	// same step. reservationOwned therefore becomes true only after cs.leases has
	// actually been incremented, so a provider method (Busy/Alive) panicking
	// before this point can never make the recovery defer release a lease this
	// invocation does not hold — protecting a concurrent turn or callback lease.
	acquire := func() {
		c.acquireLeaseLocked(cs)
		reservationOwned = true
	}
	// Validate the session and derive its memory scope before any cache lookup or
	// runner creation. An invalid session (missing owner, malformed group id) must
	// fail closed here so a runner is never installed over an unusable scope.
	memSess, err := info.MemoryScope()
	if err != nil {
		return runnerSelection{}, fmt.Errorf("session scope: %w", err)
	}

	var (
		stale           Runner
		newRunner       NewRunnerFunc
		hooksFn         func() []hooks.HookPlugin
		defaultModel    string
		defaultThinking ai.ThinkingLevel
		delegateRunner  delegatetool.SessionRunner
		cachedModel     string
		cachedThinking  ai.ThinkingLevel
		selected        bool
	)
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		var ok bool
		cs, ok = c.sessions[info.ID]
		if !ok {
			cs = &cachedSession{info: info}
			c.sessions[info.ID] = cs
			created = true
		}
		wasReserved := c.using(cs)
		if cs.failedAdmission && cs.r == nil && !wasReserved {
			// The bad runner was already detached by another lifecycle path.
			// Nothing cache-reachable remains to quarantine.
			cs.failedAdmission = false
			cs.stale = false
		}

		// A failed admission is a stronger state than ordinary stale: its
		// Alive/Busy method may be the panic source, so branch before invoking
		// either method. A defensive reservation remains owned by its turn.
		if cs.r != nil && cs.failedAdmission {
			if wasReserved {
				cs.stale = true
				if reserve {
					acquire()
				}
				selection = runnerSelection{session: cs, runner: cs.r, model: cs.model, thinking: cs.thinking}
				selected = true
				return
			}
			stale = cs.r
			cs.r = nil
			cs.stale = false
			cs.failedAdmission = false
		}
		// Reservation is authoritative. Do not inspect a runner owned by an
		// admitted turn; only cache/request metadata may make its successor stale.
		if cs.r != nil && wasReserved {
			if len(extraTools) > 0 || (model != "" && cs.model != model) ||
				(thinking != "" && cs.thinking != thinking) {
				cs.stale = true
			}
			if reserve {
				acquire()
			}
			selection = runnerSelection{session: cs, runner: cs.r, model: cs.model, thinking: cs.thinking}
			selected = true
			return
		}
		// A prior ordinary stale runner may still have a valid Busy method.
		if cs.r != nil && cs.stale {
			if cs.r.Busy() {
				if reserve {
					acquire()
				}
				selection = runnerSelection{session: cs, runner: cs.r, model: cs.model, thinking: cs.thinking}
				selected = true
				return
			}
			stale = cs.r
			cs.r = nil
			cs.stale = false
			cs.failedAdmission = false
		}
		if cs.r != nil {
			replace := cs.stale || len(extraTools) > 0 || !cs.r.Alive() ||
				(model != "" && cs.model != model) || (thinking != "" && cs.thinking != thinking)
			if replace && cs.r.Busy() {
				// A runner selected by an admitted turn is owned by that turn even
				// before Runner.Chat reports Busy. Every non-terminal replacement
				// path defers to it and makes the following turn rebuild instead.
				cs.stale = true
				if reserve {
					acquire()
				}
				selection = runnerSelection{session: cs, runner: cs.r, model: cs.model, thinking: cs.thinking}
				selected = true
				return
			}
			switch {
			case cs.stale:
				stale = cs.r
				cs.r = nil
				cs.stale = false
				cs.failedAdmission = false
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
			default:
				if reserve {
					acquire()
				}
				selection = runnerSelection{session: cs, runner: cs.r, model: cs.model, thinking: cs.thinking}
				selected = true
				return
			}
		}
		// Mark the lease before runner construction releases c.mu. This covers a
		// reset/reaper/invalidation interleaving while the factory is running.
		if reserve {
			acquire()
		}
		newRunner = c.newRunner
		hooksFn = c.hooksFn
		defaultModel = c.defaultModel
		defaultThinking = c.defaultThinking
		delegateRunner = c.delegateRunner
		cachedModel = cs.model
		cachedThinking = cs.thinking
	}()
	if selected {
		return selection, nil
	}

	if stale != nil {
		_ = c.closeRetired(stale)
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
		GuestID:        info.GuestID,
		SessionID:      info.ID,
		AgentID:        info.AgentID,
		ProjectID:      info.ProjectID,
		HooksFn:        hooksFn,
		ExtraTools:     extraTools,
		DelegateRunner: delegateRunner,
	})
	if err != nil {
		c.mu.Lock()
		// The empty selection returned here is never handed to a caller, so this
		// invocation must release the lease it acquired for construction; leaving
		// it would permanently pin the record when a concurrent goroutine installs
		// a runner on the same session.
		if reservationOwned {
			c.releaseLeaseLocked(cs)
			reservationOwned = false
		}
		if current := c.sessions[info.ID]; current == cs && cs.r == nil && !c.using(cs) {
			delete(c.sessions, info.ID)
		}
		c.mu.Unlock()
		return runnerSelection{}, err
	}

	c.mu.Lock()
	if c.sessions[info.ID] != cs {
		// A terminal close detached this construction while its factory ran. Never
		// resurrect the record or hand its runner to a callback/turn after that
		// fencing point.
		if reservationOwned {
			c.releaseLeaseLocked(cs)
			reservationOwned = false
		}
		c.mu.Unlock()
		_ = c.closeRetired(r)
		return runnerSelection{}, errors.New("runner admission closed")
	}
	if cs.r != nil {
		// Another goroutine installed a runner; discard ours.
		selection := runnerSelection{session: cs, runner: cs.r, model: cs.model, thinking: cs.thinking}
		c.mu.Unlock()
		_ = c.closeRetired(r)
		return selection, nil
	}
	cs.r = r
	// reset/invalidation may have marked this reserved lease stale while its
	// factory ran. Keep the turn's immutable selection below, but never restore
	// reset-cleared cache metadata for the next turn.
	if !cs.stale {
		cs.model = effectiveModel
		cs.thinking = effectiveThinking
		cs.failedAdmission = false
	}
	c.mu.Unlock()

	// Bootstrap memory for this session using the scope derived up front.
	if err := c.mem.Bootstrap(ctx, memSess); err != nil {
		c.log.Warn("memory bootstrap failed", "session_id", info.ID, "error", err)
	}

	c.log.Info("created runner", "session_id", info.ID, "model", effectiveModel)
	return runnerSelection{session: cs, runner: r, model: effectiveModel, thinking: effectiveThinking}, nil
}

func (c *runnerCache) reserve(cs *cachedSession) {
	c.mu.Lock()
	if cs != nil && c.sessions[cs.info.ID] == cs {
		c.acquireLeaseLocked(cs)
	}
	c.mu.Unlock()
}

func (c *runnerCache) releaseReservation(cs *cachedSession) {
	c.mu.Lock()
	if cs != nil && c.sessions[cs.info.ID] == cs {
		c.releaseLeaseLocked(cs)
	}
	c.mu.Unlock()
}

func (c *runnerCache) acquireFilesystemUse(ctx context.Context, info session.Info) (runnerSelection, error) {
	return c.getOrCreateReserved(ctx, info, "", "")
}

func (c *runnerCache) releaseFilesystemUse(cs *cachedSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cs != nil && c.sessions[cs.info.ID] == cs {
		c.releaseLeaseLocked(cs)
	}
}

func (c *runnerCache) acquireLeaseLocked(cs *cachedSession) { cs.leases++ }
func (c *runnerCache) releaseLeaseLocked(cs *cachedSession) {
	if cs.leases > 0 {
		cs.leases--
	}
}
func (c *runnerCache) using(cs *cachedSession) bool { return cs.leases > 0 }

// recoverFailedAdmission unwinds a synchronous admission whose construction or a
// provider probe panicked. It releases a lease only when this invocation owned
// one (owned), so a panic before this invocation acquired never decrements a
// concurrent turn's or callback's lease. An installed runner is quarantined
// rather than closed under an unknown panic; an empty record this invocation
// created is dropped only when nothing else still leases it.
func (c *runnerCache) recoverFailedAdmission(cs *cachedSession, created, owned bool) {
	if cs == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A terminal close may have detached cs while construction ran. Its owner
	// still releases exactly its own local lease; it must not depend on cache
	// reachability, which terminal close intentionally removes.
	if owned {
		c.releaseLeaseLocked(cs)
	}
	if c.sessions[cs.info.ID] != cs {
		return
	}
	if cs.r != nil {
		// The runner or one of its probe methods panicked; retire it for rebuild
		// without invoking it again. A surviving lease keeps it cache-reachable.
		cs.stale = true
		cs.failedAdmission = true
		cs.model = ""
		cs.thinking = ""
		return
	}
	if created && !c.using(cs) {
		delete(c.sessions, cs.info.ID)
	}
}

// quarantineLeasedRunner retires a runner whose provider panicked while a lease
// is held, without releasing that lease: the lease owner releases it exactly
// once on its own path. The runner is marked failed so the next lookup rebuilds
// without probing it. An active turn keeps its own runner snapshot and is
// unaffected; only future cache lookups observe the quarantine.
func (c *runnerCache) quarantineLeasedRunner(cs *cachedSession) {
	if cs == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions[cs.info.ID] != cs || cs.r == nil {
		return
	}
	cs.stale = true
	cs.failedAdmission = true
	cs.model = ""
	cs.thinking = ""
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
	c.releaseLeaseLocked(cs)
	if cs.r == nil {
		delete(c.sessions, cs.info.ID)
		return
	}
	cs.stale = true
	cs.failedAdmission = true
	cs.model = ""
	cs.thinking = ""
}

// closeRetired is best-effort because the cache has already detached r before
// calling it. A plugin runner must not crash a reaper/reset loop by panicking
// in Close, and no recovered value is logged because it may contain a secret.
func (c *runnerCache) closeRetired(r Runner) (err error) {
	defer func() {
		if recover() != nil {
			c.log.Error("runner close panicked")
			err = errors.New("runner close failed")
		}
	}()
	return r.Close()
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
	return errors.Join(cbErr, c.closeRetired(cs.r))
}

func (c *runnerCache) reset() error {
	return c.resetWhere(nil)
}

// resetWhere retires only idle, unreserved runners selected by include. Every
// non-terminal reset entry point uses it, so an admitted lease is handled the
// same for agent-wide and user-scoped invalidation.
func (c *runnerCache) resetWhere(include func(*cachedSession) bool) error {
	runners := make([]Runner, 0, len(c.sessions))
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, cs := range c.sessions {
			if include != nil && !include(cs) {
				continue
			}
			switch {
			case cs.failedAdmission && c.using(cs):
				cs.stale = true
			case cs.failedAdmission && cs.r != nil:
				runners = append(runners, cs.r)
				cs.r = nil
				cs.stale = false
				cs.failedAdmission = false
			case cs.failedAdmission:
				cs.stale = false
				cs.failedAdmission = false
			case c.using(cs):
				cs.stale = true
			case cs.r != nil && cs.r.Busy():
				cs.stale = true
			case cs.r != nil:
				runners = append(runners, cs.r)
				cs.r = nil
				cs.stale = false
				cs.failedAdmission = false
			}
			cs.model = ""
			cs.thinking = ""
		}
	}()

	var lastErr error
	for _, r := range runners {
		if err := c.closeRetired(r); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// invalidateSkillPolicy retires idle runners immediately and marks busy runners
// for replacement after their current turn. This is the local boundary for a
// committed AgentSkillPolicy; cross-replica digest invalidation is Phase 4.
func (c *runnerCache) invalidateSkillPolicy() error {
	runners := make([]Runner, 0, len(c.sessions))
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, cs := range c.sessions {
			// Defensive ordering: a reservation is authoritative even if another
			// cache path has temporarily cleared r. The next lookup must rebuild.
			if cs.failedAdmission && c.using(cs) {
				cs.stale = true
				continue
			}
			if cs.failedAdmission && cs.r != nil {
				runners = append(runners, cs.r)
				cs.r = nil
				cs.stale = false
				cs.failedAdmission = false
				continue
			}
			if cs.failedAdmission {
				cs.stale = false
				cs.failedAdmission = false
				continue
			}
			if c.using(cs) {
				cs.stale = true
				continue
			}
			if cs.r == nil {
				continue
			}
			if cs.r.Busy() {
				cs.stale = true
				continue
			}
			runners = append(runners, cs.r)
			cs.r = nil
			cs.stale = false
			cs.failedAdmission = false
		}
	}()
	var lastErr error
	for _, r := range runners {
		if err := c.closeRetired(r); err != nil {
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
			if err := c.closeRetired(cs.r); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

// closeWhere is terminal: unlike resetWhere it removes every matching cache
// entry and closes busy or reserved runners too. Owner deletion is allowed to
// interrupt work; ordinary policy invalidation must never use this path.
func (c *runnerCache) closeWhere(include func(*cachedSession) bool) error {
	c.mu.Lock()
	closing := make([]Runner, 0)
	for id, cs := range c.sessions {
		if !include(cs) {
			continue
		}
		delete(c.sessions, id)
		if cs.r != nil {
			closing = append(closing, cs.r)
		}
	}
	c.mu.Unlock()
	var lastErr error
	for _, r := range closing {
		if err := c.closeRetired(r); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// reap closes runners that are idle or dead.
func (c *runnerCache) reap() {
	now := time.Now()
	var closing []Runner
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for id, cs := range c.sessions {
			if cs.failedAdmission && c.using(cs) {
				cs.stale = true
				continue
			}
			if cs.failedAdmission && cs.r != nil {
				closing = append(closing, cs.r)
				cs.r = nil
				cs.stale = false
				cs.failedAdmission = false
				continue
			}
			if cs.failedAdmission {
				cs.stale = false
				cs.failedAdmission = false
				continue
			}
			if c.using(cs) {
				continue
			}
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
	}()

	for _, r := range closing {
		_ = c.closeRetired(r)
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
