package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Service bundles the v2 task system components for boot wiring. Fields are
// unexported so callers can't reach in to write task status without going
// through the transition service (D14). Use Facade for external mutations,
// Start/Stop for the dispatcher lifecycle.
type Service struct {
	queries    *sqlc.Queries
	transition *TransitionService
	facade     *ServiceFacade
	dispatcher *Dispatcher
}

// Facade returns the org-aware HTTP/CLI surface.
func (s *Service) Facade() *ServiceFacade { return s.facade }

// Start registers the dispatcher tick on the given scheduler. Pass the
// scheduler.Service (or any SchedulerLike) the gateway is already running.
// Using TickEvery.String() preserves sub-second resolution (avoiding the
// integer-truncation bug the previous duplicated wiring carried).
func (s *Service) Start(ctx context.Context, sched SchedulerLike) error {
	if sched == nil {
		return nil
	}
	return sched.ScheduleEvery(ctx, s.dispatcher.cfg.TickEvery.String(), func(ctx context.Context) {
		s.dispatcher.Tick(ctx)
	})
}

// Stop drains in-flight workers. The scheduler owns the tick lifecycle and
// stops firing on its own.
func (s *Service) Stop() { s.dispatcher.Stop() }

// BootConfig is the minimal wiring needed at server start.
//
// Pools and Memory are deliberately absent: until the agent.Pool → RunnerFunc
// adapter exists (Phase 6 follow-up) the dispatcher uses a noop runner that
// fails with a clear "not wired" message and the session minter just produces
// a UUID. Adding either field before the adapter is wired would silently drop
// caller-supplied dependencies — the exact bug pattern the PR-1 review
// surfaced. Re-introduce them as part of the adapter PR.
type BootConfig struct {
	DB *sql.DB
	// MaxPerOrg, TickEvery, LeaseTTL override defaults; zero values use the
	// dispatcher's defaults.
	MaxPerOrg int
	TickEvery time.Duration
	LeaseTTL  time.Duration
	Logger    *slog.Logger
}

// New constructs every v2 component. The dispatcher is constructed but not
// started; the caller invokes Service.Start with a scheduler.
//
// PHASE 6 STATUS: the runner is a noop that fails with a "worker integration
// not wired" message. Replacing it with a real agent.Pool/Runner adapter
// (which translates Runner.Chat events into TaskControlTool calls,
// registers task_control as a tool, and pumps the event channel until
// completion) is the remaining engineering work for Slice 1's end-to-end
// story. Tracked as a follow-up — see plan.md Phase 6 handoff.
func New(cfg BootConfig) *Service {
	q := sqlc.New(cfg.DB)
	svc := NewTransitionService(cfg.DB, q)
	facade := NewServiceFacade(cfg.DB, q, svc)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "tasks")
	}

	disp := NewDispatcher(DispatcherConfig{
		Service:    svc,
		Queries:    q,
		Runner:     noopRunner(logger),
		Resolver:   sessionAndCreatorResolver(logger),
		NewSession: sessionMinterFor(logger),
		MaxPerOrg:  cfg.MaxPerOrg,
		TickEvery:  cfg.TickEvery,
		LeaseTTL:   cfg.LeaseTTL,
		Logger:     logger.With("subcomponent", "dispatcher"),
	})

	return &Service{
		queries:    q,
		transition: svc,
		facade:     facade,
		dispatcher: disp,
	}
}

// noopRunner returns a RunnerFunc that always fails. Used until the real
// agent.Runner adapter is wired (Phase 6 follow-up).
func noopRunner(log *slog.Logger) RunnerFunc {
	return func(_ context.Context, run RunContext, tool *TaskControlTool) error {
		log.Warn("tasks v2 noop runner invoked",
			"task_id", run.TaskID, "run_id", run.RunID,
			"hint", "wire the agent.Pool adapter in tasks.New to actually execute tasks")
		return tool.Fail(context.Background(),
			"task system v2 runner not wired (noop): agent.Pool adapter is the Phase 6 follow-up",
			false)
	}
}

// sessionAndCreatorResolver covers steps 2 and 3 of D13's executor
// resolution. The dispatcher consults the dispatch_hint table itself before
// invoking the resolver.
//
//  2. session-derived agent: if task.session_id is set, load the session and
//     return its owning agent_id.
//  3. creator fallback: task.agent_id.
func sessionAndCreatorResolver(log *slog.Logger) ExecutorResolver {
	return func(_ context.Context, task sqlc.AgentTask) (string, bool) {
		// Session-derived resolution is wired alongside the real runner; for
		// the noop boot we just use the creator fallback. The dispatcher
		// applies the creator fallback when this returns (false), so this
		// implementation deliberately punts.
		_ = log
		_ = task
		return "", false
	}
}

// sessionMinterFor returns a SessionMinter that produces UUID-tagged session
// ids. The real agent.Runner integration owns session bootstrap; for the
// Slice 1 boot we just supply a unique identifier so the run row's
// session_id has a value. The agent adapter (Phase 6 follow-up) replaces
// this with one that hooks into memory.Provider's session creation.
func sessionMinterFor(_ *slog.Logger) SessionMinter {
	return func(_ context.Context, task sqlc.AgentTask) (string, error) {
		if task.UserID == "" {
			return "", fmt.Errorf("task has no user_id; cannot mint session")
		}
		return "task-" + uuid.NewString(), nil
	}
}
