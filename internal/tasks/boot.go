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

// Service bundles the v2 task system components for boot wiring.
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
	// MaxWorkers, TickEvery, LeaseTTL override defaults; zero values use the
	// dispatcher's defaults.
	MaxWorkers int
	TickEvery  time.Duration
	LeaseTTL   time.Duration
	Logger     *slog.Logger
}

// New constructs the task system. The dispatcher is constructed but not
// started; the caller registers it on a scheduler via dispatcher.Start.
//
// If BootConfig.Pools is non-nil, the dispatcher uses PoolAdapter to drive
// real agent.Runner instances. Otherwise it falls back to a noop runner
// that fails with a clear message — used by tests and by boots that
// intentionally skip agent wiring.
func New(cfg BootConfig) *Service {
	q := sqlc.New(cfg.DB)
	svc := NewTransitionService(cfg.DB, q)
	facade := NewServiceFacade(cfg.DB, q, svc)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "tasks")
	}

	var runner RunnerFunc
	if cfg.Pools != nil {
		runner = NewPoolAdapter(cfg.Pools, cfg.Memory, logger).AsRunnerFunc(q)
	} else {
		runner = noopRunner(logger)
	}

	disp := NewDispatcher(DispatcherConfig{
		Service:    svc,
		Queries:    q,
		Runner:     runner,
		Resolver:   sessionAndCreatorResolver(q, cfg.Memory, logger),
		NewSession: sessionMinterFor(cfg.Memory, logger),
		MaxWorkers: cfg.MaxWorkers,
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
// invoking this resolver, and applies the creator fallback (task.agent_id)
// when this returns (false), so this implementation only needs to handle
// the session-derived case.
//
// Session-derived resolution: we'd need to load the session row, find its
// agent. For Slice 1 we don't have that lookup wired (memory.Provider
// doesn't expose session→agent today). The dispatcher's creator-fallback
// branch covers the common case; this returning (false) just defers there.
func sessionAndCreatorResolver(_ *sqlc.Queries, _ memory.Provider, _ *slog.Logger) ExecutorResolver {
	return func(_ context.Context, _ sqlc.AgentTask) (string, bool) {
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
