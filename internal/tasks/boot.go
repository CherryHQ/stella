package tasks

import (
	"context"
	"database/sql"
	"errors"
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
		newSession = legacySessionMinter(cfg.Memory, q, logger)
	}
	facade := NewServiceFacade(cfg.DB, q, svc, newSession)

	disp := NewDispatcher(DispatcherConfig{
		Service:    svc,
		Queries:    q,
		Executor:   exec,
		Resolver:   sessionOwnerResolver(q, logger),
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

// sessionOwnerResolver resolves the executor for a task that has already run by
// reusing the executor_agent_id of its latest worker run. This sits between the
// dispatch hint and owner-agent fallback in the dispatcher's precedence chain
// (D13), so a retry keeps the original executor and runs in the task's
// persisted session — even when the first run was handed to another agent via a
// dispatch hint.
//
// It resolves from durable task-run data rather than memory.SessionManager:
// LoadInfo requires user_id+agent_id already in the context scope, but the
// dispatcher's scheduler context has neither, and agent_id was precisely the
// owner the old resolver tried to discover — a circular dependency that made the
// memory-backed lookup fail and silently fall back to the owner-agent path.
// Returns (false) when the task has no recorded session/run or that run carried
// no executor, letting the dispatcher fall back to task.agent_id.
func sessionOwnerResolver(q *sqlc.Queries, logger *slog.Logger) ExecutorResolver {
	return func(ctx context.Context, task sqlc.AgentTask) (string, bool) {
		if task.SessionID == "" {
			return "", false
		}
		run, err := q.LatestAgentTaskRunForTask(ctx, sqlc.LatestAgentTaskRunForTaskParams{
			TaskID: nullable(task.ID), Kind: RunKindWorker,
		})
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				logger.Warn("tasks: latest run lookup failed", "task", task.ID, "err", err)
			}
			return "", false
		}
		if !run.ExecutorAgentID.Valid || run.ExecutorAgentID.String == "" {
			return "", false
		}
		return run.ExecutorAgentID.String, true
	}
}

// registrySessionMinter creates task sessions through the session.Registry for
// the resolved agent. Sessions are created with KindTask/ChannelTask so they
// are excluded from user-facing session lists and review candidates.
func registrySessionMinter(sm agent.ServiceManager, _ *slog.Logger) SessionMinter {
	return func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		if userID == "" {
			return "", fmt.Errorf("task has no user_id; cannot mint session")
		}
		if agentID == "" {
			return "", fmt.Errorf("task has no agent_id; cannot mint session")
		}
		svc := sm.GetService(agentID)
		if svc == nil {
			return "", fmt.Errorf("no service for executor agent %q", agentID)
		}
		info, err := svc.MintTaskSession(ctx, userID, agentID, projectID)
		if err != nil {
			return "", err
		}
		return info.ID, nil
	}
}

// legacySessionMinter is the pre-registry fallback used when ServiceManager is nil.
func legacySessionMinter(mem memory.Provider, q *sqlc.Queries, _ *slog.Logger) SessionMinter {
	return func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		if userID == "" {
			return "", fmt.Errorf("task has no user_id; cannot mint session")
		}
		if agentID == "" {
			return "", fmt.Errorf("task has no agent_id; cannot mint session")
		}
		sessionID := legacyMintSession(userID)
		now := time.Now().UTC()
		if sm, ok := mem.(memory.SessionManager); ok {
			if err := sm.SaveInfo(ctx, memory.SessionInfo{
				ID: sessionID, UserID: userID, AgentID: agentID, ProjectID: projectID,
				Kind: "task", Channel: "task", CreatedAt: now, LastActive: now,
			}); err != nil {
				return "", err
			}
		} else {
			if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
				ID: uuid.NewString(), SessionID: sessionID,
				Title:   sql.NullString{String: "Task", Valid: true},
				Channel: "task", Kind: "task", ProjectID: nullable(projectID), Archived: 0,
				LastActive: now.Format("2006-01-02 15:04:05"),
				AgentID:    nullable(agentID), UserID: nullable(userID),
			}); err != nil {
				return "", err
			}
		}
		return sessionID, nil
	}
}

func legacyMintSession(_ string) string {
	return "task-" + uuid.NewString()
}
