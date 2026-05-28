package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Service bundles the v2 task system components for boot wiring. The legacy
// stub Service in service.go is kept for back-compat with the existing
// HTTP handlers (which still return 503); cmd/stella holds both side-by-side
// until Phase 6 of the API rewrite swaps them.
type Service struct {
	Queries    *sqlc.Queries
	Transition *TransitionService
	Facade     *ServiceFacade
	Dispatcher *Dispatcher
}

// BootConfig is the minimal wiring needed at server start.
type BootConfig struct {
	DB     *sql.DB
	Memory memory.Provider // used to mint sessions
	Pools  func(agentID string) (agent.NewRunnerFunc, bool)
	// MaxPerOrg, TickEvery, LeaseTTL override defaults; zero values use the
	// dispatcher's defaults.
	MaxPerOrg int
	TickEvery time.Duration
	LeaseTTL  time.Duration
	Logger    *slog.Logger
}

// BuildService constructs every v2 component. The dispatcher is constructed
// but not started; the caller registers it on a scheduler via
// dispatcher.Start(ctx, sched).
//
// PHASE 6 STATUS: this wires the dispatcher to a noop RunnerFunc that fails
// with a "worker integration not wired" message. Replacing the noop with a
// real agent.Pool/Runner adapter (which would translate Runner.Chat events
// into TaskControlTool calls, register task_control as a tool, and pump the
// event channel until completion) is the remaining engineering work for
// Slice 1's end-to-end story. Tracked as a follow-up — see plan.md Phase 6
// handoff.
func New(cfg BootConfig) *Service {
	q := sqlc.New(cfg.DB)
	svc := NewTransitionService(cfg.DB, q)
	facade := NewServiceFacade(cfg.DB, q, svc)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "tasks")
	}

	runner := noopRunner(logger)

	disp := NewDispatcher(DispatcherConfig{
		Service:    svc,
		Queries:    q,
		Runner:     runner,
		Resolver:   sessionAndCreatorResolver(q, cfg.Memory, logger),
		NewSession: sessionMinterFor(cfg.Memory, logger),
		MaxPerOrg:  cfg.MaxPerOrg,
		TickEvery:  cfg.TickEvery,
		LeaseTTL:   cfg.LeaseTTL,
		Logger:     logger.With("subcomponent", "dispatcher"),
	})

	return &Service{
		Queries:    q,
		Transition: svc,
		Facade:     facade,
		Dispatcher: disp,
	}
}

// noopRunner returns a RunnerFunc that always fails. Used until the real
// agent.Runner adapter is wired (Phase 6 follow-up).
func noopRunner(log *slog.Logger) RunnerFunc {
	return func(_ context.Context, run sqlc.AgentTaskRun, tool *TaskControlTool) error {
		log.Warn("tasks v2 noop runner invoked",
			"task_id", run.TaskID.String, "run_id", run.ID,
			"hint", "wire BootConfig.Pools to a real agent adapter to actually execute tasks")
		return tool.Fail(context.Background(),
			"task system v2 runner not wired (noop): connect cmd/stella to agent.PoolManager",
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
func sessionAndCreatorResolver(_ *sqlc.Queries, _ memory.Provider, log *slog.Logger) ExecutorResolver {
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
func sessionMinterFor(_ memory.Provider, _ *slog.Logger) SessionMinter {
	return func(_ context.Context, task sqlc.AgentTask) (string, error) {
		if task.UserID == "" {
			return "", fmt.Errorf("task has no user_id; cannot mint session")
		}
		return "task-" + uuid.NewString(), nil
	}
}
