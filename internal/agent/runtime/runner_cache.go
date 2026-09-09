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
	// closing blocks new admissions while terminal Close is in flight.
	closing   bool
	operation chan struct{}
	// unusable quarantines a runner after an admission panic or failed Close.
	// Such runners may only be retried through Close; Alive and Busy are unsafe.
	unusable bool
	// reserved is set synchronously during turn admission, before the chat
	// goroutine invokes Runner.Chat. Policy invalidation treats it like Busy so
	// an admitted turn keeps its immutable runner snapshot through that gap.
	reserved bool
	building *RunnerBuildOwner
}

// runnerSelection is the immutable admission lease for one turn. Cache fields
// remain mutable for the next turn (reset intentionally clears them), so code
// executing an admitted turn must use this value rather than cachedSession.
type runnerSelection struct {
	session           *cachedSession
	runner            Runner
	factoryGeneration uint64
	model             string
	thinking          ai.ThinkingLevel
	pluginContext     PluginContext
	beforeRun         BeforeRunFunc
	snapshotPrompt    SnapshotPromptFunc
}

func runnerSelectionFor(cs *cachedSession, generation uint64) runnerSelection {
	return runnerSelection{
		session:           cs,
		runner:            cs.r,
		factoryGeneration: generation,
		model:             cs.model,
		thinking:          cs.thinking,
		pluginContext:     cs.r.PluginContext(),
	}
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
	// factoryGeneration changes whenever the builder or its immutable inputs
	// change. A build captures it before doing slow I/O and may publish only if
	// it is still current.
	factoryGeneration uint64
	// retired owns every detached runner from registration through successful
	// Close. Entries stay enumerable while Close runs, so cleanup cannot lose
	// the only owner during a slow close.
	retired []*retiredRunner
	mu      sync.Mutex
	log     *slog.Logger
}

// maxConcurrentRunnerCloses bounds Docker and filesystem cleanup pressure
// during a large invalidation while avoiding serial idle-runner retirement.
const maxConcurrentRunnerCloses = 8

type retiredRunner struct {
	runner     Runner
	owner      *RunnerBuildOwner
	session    *cachedSession
	incomplete bool
	closing    bool
	done       chan struct{}
}

// retireLocked publishes ownership before the cache lock is released. The
// entry remains visible while Close runs and is removed only after success.
func (c *runnerCache) retireLocked(r Runner) *retiredRunner {
	if r != nil {
		for _, entry := range c.retired {
			if entry.runner == r {
				return entry
			}
		}
		entry := &retiredRunner{runner: r}
		for _, cs := range c.sessions {
			if cs.r == r {
				entry.session = cs
				break
			}
		}
		c.retired = append(c.retired, entry)
		return entry
	}
	return nil
}

func (c *runnerCache) retireBuildLocked(owner *RunnerBuildOwner) *retiredRunner {
	if owner == nil {
		return nil
	}
	for _, entry := range c.retired {
		if entry.owner == owner {
			return entry
		}
	}
	entry := &retiredRunner{owner: owner, incomplete: true}
	for _, cs := range c.sessions {
		if cs.building == owner {
			entry.session = cs
			break
		}
	}
	c.retired = append(c.retired, entry)
	return entry
}

// retireCompletedBuildLocked makes the partial runner reachable from the same
// retired owner whether teardown started before construction returned or the
// factory result was discarded by a concurrent publication. A failed factory
// still needs this entry because its partial runner owns sandbox/tools/hooks.
func (c *runnerCache) retireCompletedBuildLocked(owner *RunnerBuildOwner, runner Runner, cs *cachedSession) {
	if owner == nil {
		return
	}
	for _, entry := range c.retired {
		if entry.owner != owner {
			continue
		}
		entry.runner = runner
		entry.session = cs
		entry.incomplete = false
		return
	}
	entry := c.retireBuildLocked(owner)
	entry.runner = runner
	entry.session = cs
	entry.incomplete = false
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

// getOrCreateReservedAtGeneration binds a prepared plugin context to the
// factory generation observed before its slow build. A mismatch is returned to
// the admission layer so it can refresh the context before retrying.
func (c *runnerCache) getOrCreateReservedAtGeneration(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, generation uint64, extraTools ...tools.Tool) (runnerSelection, error) {
	return c.withOperation(ctx, info, func() (runnerSelection, error) {
		return c.getOrCreateWithReservationAttempt(ctx, info, model, thinking, true, extraTools, &generation)
	})
}

func (c *runnerCache) getOrCreateWithReservation(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, reserve bool, extraTools ...tools.Tool) (selection runnerSelection, err error) {
	return c.withOperation(ctx, info, func() (runnerSelection, error) {
		return c.getOrCreateWithReservationUnserialized(ctx, info, model, thinking, reserve, extraTools...)
	})
}

func (c *runnerCache) withOperation(ctx context.Context, info session.Info, fn func() (runnerSelection, error)) (selection runnerSelection, err error) {
	if _, err := info.MemoryScope(); err != nil {
		return runnerSelection{}, fmt.Errorf("session scope: %w", err)
	}
	cs, err := c.acquire(ctx, info)
	if err != nil {
		return runnerSelection{}, err
	}
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if cs.closing && err == nil {
			cs.reserved = false
			selection = runnerSelection{}
			err = errors.New("runner termination is pending")
		}
		if err != nil && !cs.closing && cs.r == nil && cs.building == nil && !c.hasRetiredSessionLocked(cs) && c.sessions[info.ID] == cs {
			delete(c.sessions, info.ID)
		}
		close(cs.operation)
		cs.operation = nil
	}()
	return fn()
}

func (c *runnerCache) acquire(ctx context.Context, info session.Info) (*cachedSession, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
			for _, entry := range c.retired {
				if entry.session == cs && entry.closing {
					pending = entry.done
					break
				}
			}
		}
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
	if cs != nil && cs.operation != nil {
		close(cs.operation)
		cs.operation = nil
	}
}

func (c *runnerCache) getOrCreateWithReservationUnserialized(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, reserve bool, extraTools ...tools.Tool) (selection runnerSelection, err error) {
	for retries := 0; ; retries++ {
		selection, err = c.getOrCreateWithReservationAttempt(ctx, info, model, thinking, reserve, extraTools, nil)
		if !errors.Is(err, ErrRunnerFactoryChanged) {
			return selection, err
		}
		if reserve {
			c.mu.Lock()
			cs := c.sessions[info.ID]
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				c.abortReservedAdmission(cs)
				return runnerSelection{}, err
			}
			if retries >= maxFactoryGenerationRetries {
				c.abortReservedAdmission(cs)
				return runnerSelection{}, fmt.Errorf("runner factory changed too frequently after %d retries (ceiling %d): %w", retries+1, maxFactoryGenerationRetries, ErrRunnerFactoryChanged)
			}
		}
		if !reserve && retries >= maxFactoryGenerationRetries {
			return runnerSelection{}, fmt.Errorf("runner factory changed too frequently after %d retries (ceiling %d): %w", retries+1, maxFactoryGenerationRetries, ErrRunnerFactoryChanged)
		}
		if err := ctx.Err(); err != nil {
			return runnerSelection{}, err
		}
	}
}

const maxFactoryGenerationRetries = 8

// ErrRunnerFactoryChanged indicates that a slow build crossed a factory
// publication boundary. The outer admission loop retries with a bounded,
// context-aware ceiling so a hot reload cannot recurse or leak a reservation.
var ErrRunnerFactoryChanged = errors.New("runner factory changed during construction")

func (c *runnerCache) getOrCreateWithReservationAttempt(ctx context.Context, info session.Info, model string, thinking ai.ThinkingLevel, reserve bool, extraTools []tools.Tool, expectedGeneration *uint64) (selection runnerSelection, err error) {
	preparedContext, preparedContextReady := PreparedPluginContext(ctx)
	var (
		cs               *cachedSession
		created          bool
		reservationOwned bool
		buildOwner       *RunnerBuildOwner
		builtRunner      Runner
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
		if reservationOwned {
			c.abortReservedAdmission(cs)
		} else if cs != nil {
			c.mu.Lock()
			if c.sessions[info.ID] == cs {
				if cs.r == nil && !cs.reserved && created {
					// No runner escaped the failed admission; do not retain an
					// empty reserved cache record.
					delete(c.sessions, info.ID)
				}
			}
			c.mu.Unlock()
		}
		if buildOwner != nil {
			buildOwner.Complete()
			c.mu.Lock()
			if builtRunner != nil && cs != nil && c.sessions[info.ID] == cs && cs.r == builtRunner {
				// The runner was already published before a later bootstrap or
				// metadata callback panicked. It remains cache-reachable under the
				// failed-admission fence, so its build owner is no longer retired.
				cs.building = nil
			} else {
				c.retireCompletedBuildLocked(buildOwner, builtRunner, cs)
				if cs != nil && c.sessions[info.ID] == cs {
					cs.building = nil
				}
			}
			c.mu.Unlock()
			_ = c.closeRetiredBatch()
		}
		selection = runnerSelection{}
		err = errors.New("runner construction failed")
	}()
	c.mu.Lock()
	cs = c.sessions[info.ID]
	c.mu.Unlock()
	// Failed partial builds can own execution without publishing a Runner.
	// Every generation retry must finish that retirement before rebuilding.
	if err := c.closeRetiredBatchFiltered(func(entry *retiredRunner) bool { return entry.session == cs }); err != nil {
		return runnerSelection{}, err
	}
	c.mu.Lock()
	pendingRetirement := c.hasRetiredSessionLocked(cs)
	c.mu.Unlock()
	if pendingRetirement {
		return runnerSelection{}, errors.New("runner termination is pending")
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
		generation      uint64
		selected        bool
	)
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if expectedGeneration != nil && c.factoryGeneration != *expectedGeneration {
			err = ErrRunnerFactoryChanged
			return
		}

		var ok bool
		cs, ok = c.sessions[info.ID]
		if !ok {
			cs = &cachedSession{info: info}
			c.sessions[info.ID] = cs
			created = true
		}
		if cs.closing {
			err = errors.New("runner termination is pending")
			return
		}
		wasReserved := cs.reserved
		reservationOwned = reserve && !wasReserved
		if cs.unusable && cs.r == nil && !wasReserved {
			// The bad runner was already detached by another lifecycle path.
			// Nothing cache-reachable remains to quarantine.
			cs.unusable = false
			cs.stale = false
		}

		// A failed admission is a stronger state than ordinary stale: its
		// Alive/Busy method may be the panic source, so branch before invoking
		// either method. A defensive reservation remains owned by its turn.
		if cs.r != nil && cs.unusable {
			if wasReserved {
				cs.stale = true
				if reserve {
					cs.reserved = true
				}
				selection = runnerSelectionFor(cs, c.factoryGeneration)
				selected = true
				return
			}
			stale = cs.r
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
		}
		// Reservation is authoritative. Do not inspect a runner owned by an
		// admitted turn; only cache/request metadata may make its successor stale.
		if cs.r != nil && wasReserved {
			if len(extraTools) > 0 || (model != "" && cs.model != model) ||
				(thinking != "" && cs.thinking != thinking) {
				cs.stale = true
			}
			selection = runnerSelectionFor(cs, c.factoryGeneration)
			selected = true
			return
		}
		// A prior ordinary stale runner may still have a valid Busy method.
		if cs.r != nil && cs.stale && !cs.unusable {
			if cs.r.Busy() {
				if reserve {
					cs.reserved = true
				}
				selection = runnerSelectionFor(cs, c.factoryGeneration)
				selected = true
				return
			}
			stale = cs.r
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
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
					cs.reserved = true
				}
				selection = runnerSelectionFor(cs, c.factoryGeneration)
				selected = true
				return
			}
			switch {
			case cs.stale:
				stale = cs.r
				c.retireLocked(cs.r)
				cs.r = nil
				cs.stale = false
				cs.unusable = false
			case len(extraTools) > 0:
				stale = cs.r
				c.retireLocked(cs.r)
				cs.r = nil
			case !cs.r.Alive():
				c.log.Warn("replacing dead runner", "session_id", info.ID)
				stale = cs.r
				c.retireLocked(cs.r)
				cs.r = nil
			case model != "" && cs.model != model:
				c.log.Info("switching model", "session_id", info.ID, "from", cs.model, "to", model)
				stale = cs.r
				c.retireLocked(cs.r)
				cs.r = nil
			case thinking != "" && cs.thinking != thinking:
				c.log.Info("switching thinking level", "session_id", info.ID, "from", cs.thinking, "to", thinking)
				stale = cs.r
				c.retireLocked(cs.r)
				cs.r = nil
			default:
				if reserve {
					cs.reserved = true
				}
				selection = runnerSelectionFor(cs, c.factoryGeneration)
				selected = true
				return
			}
		}
		// Mark the lease before runner construction releases c.mu. This covers a
		// reset/reaper/invalidation interleaving while the factory is running.
		if reserve {
			cs.reserved = true
		}
		newRunner = c.newRunner
		buildOwner = NewRunnerBuildOwner()
		cs.building = buildOwner
		hooksFn = c.hooksFn
		defaultModel = c.defaultModel
		defaultThinking = c.defaultThinking
		delegateRunner = c.delegateRunner
		cachedModel = cs.model
		cachedThinking = cs.thinking
		generation = c.factoryGeneration
	}()
	if selected {
		return selection, nil
	}
	if err != nil {
		return runnerSelection{}, err
	}

	if stale != nil {
		closeErr := c.closeRetiredBatch()
		c.mu.Lock()
		if c.hasRetiredSessionLocked(cs) {
			closeErr = errors.Join(closeErr, errors.New("runner termination is pending"))
		}
		c.mu.Unlock()
		if closeErr != nil {
			c.mu.Lock()
			if c.sessions[info.ID] == cs && cs.building == buildOwner {
				cs.building = nil
			}
			c.mu.Unlock()
			buildOwner.Complete()
			if reservationOwned {
				c.abortReservedAdmission(cs)
			}
			return runnerSelection{}, closeErr
		}
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

	params := RunnerParams{
		Model:           effectiveModel,
		Thinking:        effectiveThinking,
		Memory:          c.mem,
		UserID:          info.UserID,
		GroupID:         info.GroupID,
		GuestID:         info.GuestID,
		ForegroundHuman: foregroundHumanSession(info),
		SessionID:       info.ID,
		AgentID:         info.AgentID,
		ProjectID:       info.ProjectID,
		HooksFn:         hooksFn,
		ExtraTools:      extraTools,
		DelegateRunner:  delegateRunner,
		BuildOwner:      buildOwner,
	}
	if preparedContextReady {
		params.PluginContext = preparedContext
		params.PluginContextReady = true
	}
	r, err := newRunner(ctx, params)
	builtRunner = r
	buildOwner.Complete()
	if err != nil {
		c.mu.Lock()
		c.retireCompletedBuildLocked(buildOwner, r, cs)
		cs.building = nil
		cs.unusable = true
		cs.stale = true
		cs.model, cs.thinking = "", ""
		c.mu.Unlock()
		closeErr := c.closeRetiredBatch()
		if reservationOwned {
			c.abortReservedAdmission(cs)
		}
		return runnerSelection{}, errors.Join(err, closeErr)
	}

	c.mu.Lock()
	if c.sessions[info.ID] == cs {
		cs.building = nil
	}
	if c.sessions[info.ID] != cs {
		// A terminal close detached the cache record while the factory ran.
		// Publish the completed partial runner directly into its retired owner;
		// never resurrect the detached session in the active map.
		c.retireCompletedBuildLocked(buildOwner, r, cs)
		c.mu.Unlock()
		_ = c.closeRetiredBatch()
		return runnerSelection{}, ErrRunnerFactoryChanged
	}
	if generation != c.factoryGeneration {
		c.retireCompletedBuildLocked(buildOwner, r, cs)
		c.mu.Unlock()
		_ = c.closeRetiredBatch()
		// Configuration changed while the factory was doing slow work. Do not
		// install the old result. The outer loop owns the reservation retry.
		if reserve {
			c.abortReservedAdmission(cs)
		}
		return runnerSelection{}, ErrRunnerFactoryChanged
	}
	if cs.closing {
		c.retireCompletedBuildLocked(buildOwner, r, cs)
		c.mu.Unlock()
		if closeErr := c.closeRetiredBatch(); closeErr != nil {
			return runnerSelection{}, closeErr
		}
		return runnerSelection{}, errors.New("runner termination is pending")
	}
	if cs.r != nil {
		// Another goroutine installed a runner; discard ours.
		selection := runnerSelectionFor(cs, c.factoryGeneration)
		c.retireCompletedBuildLocked(buildOwner, r, cs)
		c.mu.Unlock()
		_ = c.closeRetiredBatch()
		return selection, nil
	}
	cs.r = r
	// reset/invalidation may have marked this reserved lease stale while its
	// factory ran. Keep the turn's immutable selection below, but never restore
	// reset-cleared cache metadata for the next turn.
	if !cs.stale {
		cs.model = effectiveModel
		cs.thinking = effectiveThinking
		cs.unusable = false
	}
	c.mu.Unlock()

	// Bootstrap memory for this session using the scope derived up front.
	if err := c.mem.Bootstrap(ctx, memSess); err != nil {
		c.log.Warn("memory bootstrap failed", "session_id", info.ID, "error", err)
	}

	c.log.Info("created runner", "session_id", info.ID, "model", effectiveModel)
	return runnerSelection{session: cs, runner: r, factoryGeneration: generation, model: effectiveModel, thinking: effectiveThinking, pluginContext: r.PluginContext()}, nil
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

func (c *runnerCache) factoryGenerationSnapshot() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.factoryGeneration
}

func (c *runnerCache) validateReservation(selection runnerSelection) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return selection.session != nil && selection.runner != nil &&
		c.sessions[selection.session.info.ID] == selection.session &&
		selection.session.r != nil && selection.session.r == selection.runner && selection.session.reserved &&
		!selection.session.stale && selection.factoryGeneration == c.factoryGeneration
}

func (c *runnerCache) releaseReservation(cs *cachedSession) {
	retire := false
	c.mu.Lock()
	if cs != nil && c.sessions[cs.info.ID] == cs {
		cs.reserved = false
		if cs.stale && cs.r != nil {
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
			cs.model = ""
			cs.thinking = ""
			retire = true
		}
	}
	c.mu.Unlock()
	if retire {
		// A stale runner has reached the end of its admitted turn. Retire it in
		// this hand-off so the next turn never carries the old capability set.
		_ = c.closeRetiredBatch()
	}
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
	if cs.r == nil && cs.building == nil && cs.operation == nil && !cs.closing && !c.hasRetiredSessionLocked(cs) {
		delete(c.sessions, cs.info.ID)
		return
	}
	cs.stale = true
	cs.unusable = true
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

func (c *runnerCache) removeRetired(entry *retiredRunner, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.closing = false
	if entry.done != nil {
		defer close(entry.done)
		entry.done = nil
	}
	if err != nil {
		if entry.session != nil && c.sessions[entry.session.info.ID] == entry.session && entry.session.r == nil {
			entry.session.r = entry.runner
			entry.session.stale = true
			entry.session.unusable = true
		}
		return
	}
	if entry.session != nil && entry.session.r == entry.runner {
		entry.session.r = nil
	}
	for i, current := range c.retired {
		if current == entry {
			c.retired = append(c.retired[:i], c.retired[i+1:]...)
			return
		}
	}
}

func (c *runnerCache) closeRetiredEntry(entry *retiredRunner) error {
	var err error
	if entry.runner != nil {
		err = c.closeRetired(entry.runner)
	} else if entry.owner != nil {
		err = c.closeBuildOwner(entry.owner)
	}
	c.removeRetired(entry, err)
	return err
}

func (c *runnerCache) closeBuildOwner(owner *RunnerBuildOwner) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("runner build cleanup failed")
		}
	}()
	return owner.Close()
}

// closeRetiredBatch waits for every detached runner while bounding Docker and
// filesystem cleanup pressure from a large invalidation burst.
func (c *runnerCache) closeRetiredBatch() error {
	return c.closeRetiredBatchFiltered(func(*retiredRunner) bool { return true })
}

func (c *runnerCache) closeRetiredBatchNonTerminal() error {
	return c.closeRetiredBatchFiltered(func(entry *retiredRunner) bool { return entry.session == nil || !entry.session.closing })
}

func (c *runnerCache) closeRetiredBatchFiltered(include func(*retiredRunner) bool) error {
	c.mu.Lock()
	entries := make([]*retiredRunner, 0, len(c.retired))
	for _, entry := range c.retired {
		if !include(entry) {
			continue
		}
		if entry.incomplete {
			continue
		}
		if !entry.closing {
			entry.closing = true
			entry.done = make(chan struct{})
			entries = append(entries, entry)
		}
	}
	c.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	workers := min(maxConcurrentRunnerCloses, len(entries))
	jobs := make(chan *retiredRunner)
	results := make(chan error, len(entries))
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for entry := range jobs {
				results <- c.closeRetiredEntry(entry)
			}
		})
	}
	for _, entry := range entries {
		jobs <- entry
	}
	close(jobs)
	wg.Wait()
	close(results)
	var joinedErr error
	for err := range results {
		joinedErr = errors.Join(joinedErr, err)
	}
	return joinedErr
}

// close shuts down the runner for a single session.
func (c *runnerCache) close(sessionID string) error {
	return c.closeWithSandbox(sessionID, nil)
}

// closeWithSandbox invokes cb with the live runner-owned sandbox, when present,
// immediately before closing the runner. Close still runs if cb fails.
func (c *runnerCache) closeWithSandbox(sessionID string, cb SandboxSessionCallback) error {
	c.mu.Lock()
	cs := c.sessions[sessionID]
	if cs != nil {
		cs.closing = true
	}
	c.mu.Unlock()
	if cs == nil {
		return nil
	}
	return c.terminalCloseSession(cs, cb)
}

// terminalCloseSession joins construction and earlier Close attempts before
// acquiring the slot. Failed resources remain reachable and block admission.
func (c *runnerCache) terminalCloseSession(cs *cachedSession, cb SandboxSessionCallback) error {
	c.mu.Lock()
	for {
		if c.sessions[cs.info.ID] != cs {
			c.mu.Unlock()
			return nil
		}
		pending := cs.operation
		if pending == nil {
			for _, entry := range c.retired {
				if entry.session == cs && entry.closing {
					pending = entry.done
					break
				}
			}
		}
		if pending == nil {
			break
		}
		c.mu.Unlock()
		<-pending
		c.mu.Lock()
	}
	cs.operation = make(chan struct{})
	owner := c.retireLocked(cs.r)
	if owner != nil {
		owner.closing = true
		owner.done = make(chan struct{})
	}
	cs.unusable = true
	c.mu.Unlock()
	defer c.finishOperation(cs)
	var cbErr error
	if owner != nil {
		if cb != nil {
			func() {
				defer func() {
					if recover() != nil {
						cbErr = errors.New("sandbox close callback failed")
					}
				}()
				if sr, ok := owner.runner.(interface{ SandboxSession() pkgsandbox.Session }); ok {
					if sess := sr.SandboxSession(); sess != nil {
						cbErr = cb(sess)
					}
				}
			}()
		}
		cbErr = errors.Join(cbErr, c.closeRetiredEntry(owner))
	}
	err := errors.Join(cbErr, c.closeRetiredBatchFiltered(func(entry *retiredRunner) bool { return entry.session == cs && entry != owner }))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasRetiredSessionLocked(cs) || cs.building != nil {
		return errors.Join(err, errors.New("runner termination is pending"))
	}
	delete(c.sessions, cs.info.ID)
	return err
}

func (c *runnerCache) reset() error {
	return c.resetWhere(nil)
}

// detachReset marks every runner for replacement and detaches every idle
// runner while the caller still owns the admission/lifecycle barrier. Close
// runs after that barrier is released, so Docker and filesystem teardown never
// elongate the mutation critical section.
func (c *runnerCache) detachReset() func() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.factoryGeneration++
	for _, cs := range c.sessions {
		if cs.closing {
			continue
		}
		if cs.operation != nil {
			cs.stale = true
			continue
		}
		switch {
		case cs.unusable && cs.reserved:
			cs.stale = true
		case cs.unusable && cs.r != nil:
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
		case cs.unusable:
			cs.stale = false
			cs.unusable = false
		case cs.reserved:
			cs.stale = true
		case cs.r != nil && cs.r.Busy():
			cs.stale = true
		case cs.r != nil:
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
		}
		cs.model = ""
		cs.thinking = ""
	}
	return func() error { return c.closeRetiredBatchNonTerminal() }
}

// resetWhere retires only idle, unreserved runners selected by include. Every
// non-terminal reset entry point uses it, so an admitted lease is handled the
// same for agent-wide and user-scoped invalidation.
func (c *runnerCache) resetWhere(include func(*cachedSession) bool) error {
	// Scoped invalidation keeps its existing predicate; global mutation uses
	// detachReset below so it can release lifecycle before waiting for Close.
	if include == nil {
		return c.detachReset()()
	}
	return c.detachResetWhere(include)()
}

// detachResetWhere marks busy/reserved runners stale and detaches idle ones,
// returning cleanup that can run after lifecycle/admission locks are released.
func (c *runnerCache) detachResetWhere(include func(*cachedSession) bool) func() error {
	c.mu.Lock()
	for _, cs := range c.sessions {
		if !include(cs) {
			continue
		}
		if cs.closing {
			continue
		}
		if cs.operation != nil {
			cs.stale = true
			continue
		}
		switch {
		case cs.unusable && cs.reserved:
			cs.stale = true
		case cs.unusable && cs.r != nil:
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
		case cs.unusable:
			cs.stale = false
			cs.unusable = false
		case cs.reserved:
			cs.stale = true
		case cs.r != nil && cs.r.Busy():
			cs.stale = true
		case cs.r != nil:
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
		}
		cs.model = ""
		cs.thinking = ""
	}
	c.mu.Unlock()
	return func() error { return c.closeRetiredBatchNonTerminal() }
}

// invalidateSkillPolicy retires idle runners immediately and marks busy runners
// for replacement after their current turn. This is the local boundary for a
// committed AgentSkillPolicy in this single-daemon deployment.
func (c *runnerCache) invalidateSkillPolicy() error {
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, cs := range c.sessions {
			if cs.closing {
				continue
			}
			if cs.operation != nil {
				cs.stale = true
				continue
			}
			// Defensive ordering: a reservation is authoritative even if another
			// cache path has temporarily cleared r. The next lookup must rebuild.
			if cs.unusable && cs.reserved {
				cs.stale = true
				continue
			}
			if cs.unusable && cs.r != nil {
				c.retireLocked(cs.r)
				cs.r = nil
				cs.stale = false
				cs.unusable = false
				continue
			}
			if cs.unusable {
				cs.stale = false
				cs.unusable = false
				continue
			}
			if cs.reserved {
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
			c.retireLocked(cs.r)
			cs.r = nil
			cs.stale = false
			cs.unusable = false
		}
	}()
	return c.closeRetiredBatchNonTerminal()
}

// closeAll shuts down all runners.
func (c *runnerCache) closeAll() error {
	return c.detachCloseAll()()
}

// detachCloseAll marks all sessions detached and returns slow cleanup for
// execution after lifecycle/admission locks are released.
func (c *runnerCache) detachCloseAll() func() error {
	return c.detachWhere(func(*cachedSession) bool { return true })
}

// closeWhere is terminal and may interrupt busy or reserved runners.
func (c *runnerCache) closeWhere(include func(*cachedSession) bool) error {
	return c.detachWhere(include)()
}

// The synchronous part blocks admission. Slow cleanup runs after the caller
// releases lifecycle and admission locks, including on owner deletion.
func (c *runnerCache) detachWhere(include func(*cachedSession) bool) func() error {
	_, closeDetached := c.detachWhereWithIDs(include)
	return closeDetached
}

func (c *runnerCache) detachWhereWithIDs(include func(*cachedSession) bool) ([]string, func() error) {
	c.mu.Lock()
	var ids []string
	var sessions []*cachedSession
	for id, cs := range c.sessions {
		if include != nil && !include(cs) {
			continue
		}
		cs.closing = true
		ids = append(ids, id)
		sessions = append(sessions, cs)
	}
	c.mu.Unlock()
	return ids, func() error {
		var errs []error
		for _, cs := range sessions {
			errs = append(errs, c.terminalCloseSession(cs, nil))
		}
		// Older detached failures remain owned even if their cache slot is gone.
		return errors.Join(append(errs, c.closeRetiredBatchNonTerminal())...)
	}
}

func (c *runnerCache) hasRetiredSessionLocked(cs *cachedSession) bool {
	for _, entry := range c.retired {
		if entry.session == cs {
			return true
		}
	}
	return false
}

// reap closes runners that are idle or dead.
func (c *runnerCache) reap() {
	now := time.Now()
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for id, cs := range c.sessions {
			if cs.closing || cs.operation != nil {
				continue
			}
			if cs.unusable && cs.reserved {
				cs.stale = true
				continue
			}
			if cs.unusable && cs.r != nil {
				c.retireLocked(cs.r)
				cs.r = nil
				cs.stale = false
				cs.unusable = false
				continue
			}
			if cs.unusable {
				cs.stale = false
				cs.unusable = false
				continue
			}
			if cs.reserved {
				continue
			}
			if cs.r == nil || cs.r.Busy() {
				continue
			}
			lastActivity := cs.r.LastActivity()
			if !cs.r.Alive() {
				c.log.Warn("removing dead runner", "session_id", id)
				c.retireLocked(cs.r)
				cs.r = nil
				continue
			}
			if now.Sub(lastActivity) > c.idleTimeout {
				c.log.Info("reaping idle runner",
					"session_id", id,
					"idle_duration", now.Sub(lastActivity).Round(time.Second))
				c.retireLocked(cs.r)
				cs.r = nil
			}
		}
	}()

	_ = c.closeRetiredBatchNonTerminal()
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
