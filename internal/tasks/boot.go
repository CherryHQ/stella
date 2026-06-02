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
	DB       *sql.DB
	Memory   memory.Provider                                  // used to mint sessions when Services is nil
	Services agent.ServiceManager                             // registry-backed session minting
	Pools    func(agentID string) (agent.NewRunnerFunc, bool) // resolves executor agents to runner factories
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
// If BootConfig.Pools is non-nil, the dispatcher uses the agent-backed worker
// executor. Otherwise it falls back to a noop executor that fails with a clear
// message.
func New(cfg BootConfig) *Service {
	q := sqlc.New(cfg.DB)
	svc := NewTransitionService(cfg.DB, q)
	facade := NewServiceFacade(cfg.DB, q, svc)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "tasks")
	}

	var exec Executor
	if cfg.Pools != nil {
		exec = newWorkerExecutor(cfg.Pools, cfg.Memory, q, svc, logger)
	} else {
		exec = noopExecutor(logger)
	}

	var newSession SessionMinter
	if cfg.Services != nil {
		newSession = registrySessionMinter(cfg.Services, logger)
	} else {
		newSession = legacySessionMinter(cfg.Memory, logger)
	}

	disp := NewDispatcher(DispatcherConfig{
		Service:    svc,
		Queries:    q,
		Executor:   exec,
		Resolver:   sessionAndCreatorResolver(q, cfg.Memory, logger),
		NewSession: newSession,
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

// noopExecutor returns an Executor that always fails non-retryably.
func noopExecutor(log *slog.Logger) Executor {
	return executorFunc(func(_ context.Context, req Request) (Result, error) {
		log.Warn("tasks v2 noop executor invoked",
			"task_id", req.Run.TaskID.String, "run_id", req.Run.ID,
			"hint", "wire BootConfig.Pools to a real agent pool to execute tasks")
		return failResult("task system v2 executor not wired (noop): connect cmd/stella to an agent pool", false), nil
	})
}

// sessionAndCreatorResolver resolves the executor from an existing task
// session: when task.session_id is set, the session owner becomes the
// executor. This sits between the dispatch hint and the creator fallback in
// the dispatcher's precedence chain (D13), so a task that already ran in a
// session keeps running under the same owning agent. Returns (false) when no
// session is recorded or the provider can't report session ownership, letting
// the dispatcher fall back to task.agent_id.
func sessionAndCreatorResolver(_ *sqlc.Queries, mem memory.Provider, logger *slog.Logger) ExecutorResolver {
	sm, ok := mem.(memory.SessionManager)
	if !ok {
		return func(_ context.Context, _ sqlc.AgentTask) (string, bool) { return "", false }
	}
	return func(ctx context.Context, task sqlc.AgentTask) (string, bool) {
		if !task.SessionID.Valid || task.SessionID.String == "" {
			return "", false
		}
		info, err := sm.LoadInfo(ctx, task.SessionID.String)
		if err != nil {
			logger.Warn("tasks: session owner lookup failed",
				"task", task.ID, "session", task.SessionID.String, "err", err)
			return "", false
		}
		if info.AgentID == "" {
			return "", false
		}
		return info.AgentID, true
	}
}

// registrySessionMinter creates task sessions through the session.Registry for
// the executor agent. Sessions are created with KindTask/ChannelTask so they
// are excluded from user-facing session lists and review candidates.
func registrySessionMinter(sm agent.ServiceManager, _ *slog.Logger) SessionMinter {
	return func(ctx context.Context, task sqlc.AgentTask, executorAgentID string) (string, error) {
		if task.UserID == "" {
			return "", fmt.Errorf("task has no user_id; cannot mint session")
		}
		agentID := executorAgentID
		if agentID == "" {
			if task.AgentID.Valid {
				agentID = task.AgentID.String
			}
		}
		if agentID == "" {
			return "", fmt.Errorf("task has no agent_id; cannot mint session")
		}
		svc := sm.GetService(agentID)
		if svc == nil {
			return "", fmt.Errorf("no service for executor agent %q", agentID)
		}
		info, err := svc.MintTaskSession(ctx, task.UserID, agentID)
		if err != nil {
			return "", err
		}
		return info.ID, nil
	}
}

// legacySessionMinter is the pre-registry fallback used when ServiceManager is nil.
func legacySessionMinter(_ memory.Provider, _ *slog.Logger) SessionMinter {
	return func(_ context.Context, task sqlc.AgentTask, _ string) (string, error) {
		if task.UserID == "" {
			return "", fmt.Errorf("task has no user_id; cannot mint session")
		}
		return legacyMintSession(task.UserID)
	}
}

func legacyMintSession(_ string) (string, error) {
	return "task-" + uuid.NewString(), nil
}
