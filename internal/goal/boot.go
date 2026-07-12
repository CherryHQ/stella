package goal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agentaccess"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Service is the boot-level bundle the server + CLI bind to. It owns the
// GoalService (the single durable-state writer) and the dispatcher, and
// exposes the read+command surface the HTTP handlers call. Every mutating method
// delegates to GoalService so the "lifecycle is written only through a
// transition" invariant holds; reads go straight to the querier.
//
// The goal subsystem does NOT own a River client: it contributes its queue +
// worker to the single process-wide client (RegisterRiverWorker / GoalQueueConfig)
// and receives that client back as its dispatcher's enqueuer (BindRiverClient).
// The client's Start/Stop lifecycle belongs to the composition root.
type Service struct {
	Queries    *sqlc.Queries
	Goal       *GoalService
	Dispatcher *Dispatcher

	// runner executes one claimed attempt; it backs both the goal River worker
	// (RegisterRiverWorker) and is shared with nothing else. queueMaxWorkers and
	// logger are captured at Boot so the composition root can assemble the shared
	// client's worker + queue config without re-deriving them.
	runner          WorkerRunner
	queueMaxWorkers int
	logger          *slog.Logger

	// mu guards the one-shot river bind (river/started). The lock is released
	// before any external River call (PeriodicJobs().Add/Remove) so those never
	// run under it.
	mu sync.Mutex
	// river is the shared working client, injected via BindRiverClient. The
	// dispatcher uses it (as its enqueuer) to dispatch claimed attempts;
	// StartDispatchTick uses it to register the single-leader convergence tick.
	river *river.Client[pgx.Tx]
	// started flips true when StartDispatchTick runs, sealing BindRiverClient.
	started bool
}

// RegisterRiverWorker registers the goal subsystem's workers into a shared
// workers bundle used to build the process-wide River client: the attempt
// executor and the convergence-tick worker (River Phase 2b). Call before building
// the client (composition root).
func (s *Service) RegisterRiverWorker(workers *river.Workers) {
	RegisterGoalWorker(workers, s.runner, s.logger.With("subcomponent", "river"))
	RegisterGoalTickWorker(workers, s.Dispatcher, s.logger.With("subcomponent", "river-tick"))
}

// GoalQueueConfig returns the goal attempt queue name and per-node worker config
// for the composition root assembling the shared working client.
func (s *Service) GoalQueueConfig() (string, river.QueueConfig) {
	return GoalQueue, river.QueueConfig{MaxWorkers: s.queueMaxWorkers}
}

// GoalTickQueueConfig returns the convergence-tick queue and its per-node worker
// config (one worker: the tick never overlaps itself on a node). The composition
// root adds it alongside GoalQueueConfig.
func (s *Service) GoalTickQueueConfig() (string, river.QueueConfig) {
	return GoalTickQueue, river.QueueConfig{MaxWorkers: 1}
}

// BindRiverClient binds the shared working River client before StartDispatchTick
// and wires it as the dispatcher's enqueuer. One-shot pre-start bind: rejects a
// nil client (missing), a second bind (duplicate), and any bind after
// StartDispatchTick (late).
func (s *Service) BindRiverClient(c *river.Client[pgx.Tx]) error {
	if c == nil {
		return fmt.Errorf("goal: BindRiverClient requires a non-nil client")
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("goal: BindRiverClient after StartDispatchTick")
	}
	if s.river != nil {
		s.mu.Unlock()
		return fmt.Errorf("goal: river client already bound")
	}
	s.river = c
	s.mu.Unlock()
	// SetEnqueuer sets the dispatcher's enqueuer once, by the single goroutine
	// that won the bind above; run it outside the lock.
	s.Dispatcher.SetEnqueuer(c)
	return nil
}

// StartDispatchTick registers the convergence tick as a single-leader River
// periodic job, replacing the per-node in-process ticker (River Phase 2b). River
// enqueues a periodic only on the elected leader and ByState uniqueness keeps at
// most one tick live, so the cluster runs a single convergence loop; any node's
// tick worker may run a fired tick. RunOnStart fires an immediate tick on
// (re-)election so convergence resumes promptly after a failover or cold start
// rather than waiting a full interval (the cost is one extra idempotent pass on
// failover). Returns the handle for StopDispatchTick. Requires BindRiverClient first.
func (s *Service) StartDispatchTick() (rivertype.PeriodicJobHandle, error) {
	s.mu.Lock()
	cl := s.river
	if cl == nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("goal: StartDispatchTick before BindRiverClient")
	}
	s.started = true
	s.mu.Unlock()
	handle := cl.PeriodicJobs().Add(river.NewPeriodicJob(
		river.PeriodicInterval(s.Dispatcher.TickInterval()),
		func() (river.JobArgs, *river.InsertOpts) {
			return goalTickArgs{}, goalTickInsertOpts(s.Dispatcher.TickInterval())
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))
	return handle, nil
}

// StopDispatchTick removes the convergence-tick periodic so no further ticks are
// enqueued. In-flight ticks drain when the shared client stops.
func (s *Service) StopDispatchTick(handle rivertype.PeriodicJobHandle) {
	s.mu.Lock()
	cl := s.river
	s.mu.Unlock()
	if cl == nil {
		return
	}
	cl.PeriodicJobs().Remove(handle)
}

// TaskChatParams is the worker-turn request passed to BootConfig.Chat. It mirrors
// the old tasks.TaskChatParams so the stellad wiring carries over verbatim: the
// callback resolves AgentID to an agent service and runs one persisted turn in
// the goal's session. executor.go consumes this type; it is declared here
// because BootConfig.Chat is its only producer.
type TaskChatParams struct {
	AgentID   string
	UserID    string
	SessionID string
	ProjectID string
	Prompt    string
	// Decompose routes the turn to the decomposition planning session
	// (KindDelegate) instead of the worker session (KindTask). Set for
	// purpose=decomposition attempts; the two session kinds resolve differently.
	Decompose        bool
	ExtraTools       []tools.Tool
	ExcludedTools    []string
	OnSandboxSession func(sandbox.Session) error
}

// TaskChatFunc runs one worker turn through the agent service layer so the
// transcript persists to the goal's session and prior turns load as
// history. An unknown agent surfaces as an Err event on the returned channel.
type TaskChatFunc func(ctx context.Context, p TaskChatParams) <-chan agent.Event

// BootConfig is the minimal wiring needed at server start. It mirrors the old
// tasks.BootConfig: a DB handle, the agent ServiceManager for session minting,
// the Chat callback for worker turns, and the dispatcher tunables.
type BootConfig struct {
	DB           *pgxpool.Pool
	Services     agent.ServiceManager // registry-backed session minting
	Chat         TaskChatFunc         // runs persisted worker turns; nil => noop executor
	Capabilities CapabilityProbe      // nil => deterministic checks are allowed
	// MaxWorkers caps concurrent attempt executions per node on the goal River
	// queue (0 => defaultGoalMaxWorkers). TickEvery/LeaseTTL override the
	// dispatcher/service defaults; zero values use them.
	MaxWorkers    int
	TickEvery     time.Duration
	LeaseTTL      time.Duration
	Logger        *slog.Logger
	ExcludedTools []string
	// AgentAccess is the trusted durable-worker PEP. A goal attempt must not
	// invoke an executor agent unless its persisted owner/executor pair passes a
	// fresh Agent execute decision.
	AgentAccess *agentaccess.Service
}

// Boot constructs the goal system and returns the bound bundle. The dispatcher is
// built but not ticking; the composition root injects the shared client via
// BindRiverClient and the server registers the single-leader tick via
// StartDispatchTick.
//
// (Named Boot, not New: the package's GoalService constructor already owns
// New(db, q, …). This is the bundle/wiring entry the server binds to.)
//
// Wiring: the worker executor (agent-backed when Chat is non-nil, else a noop that
// fails non-retryably) plus the worker + planning session minters are registered
// on the GoalService; one Worker drives claimed attempts and the
// Dispatcher schedules the convergence loop over it.
func Boot(cfg BootConfig) (*Service, error) {
	q := sqlc.New(cfg.DB)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "goal")
	}

	svc := New(cfg.DB, q,
		WithExecutor(bootExecutor(cfg.Chat, logger, cfg.ExcludedTools, cfg.AgentAccess)),
		WithCheckRunner(NewCheckRunner(q, 0)),
		WithCapabilityProbe(cfg.Capabilities),
		WithSessionMinter(RegistrySessionMinter(cfg.Services)),
		WithPlanningSessionMinter(RegistryPlanningSessionMinter(cfg.Services)),
		WithSessionDisposer(RegistrySessionDisposer(cfg.Services)),
		WithConfig(Config{LeaseTTL: cfg.LeaseTTL}),
	)

	maxWorkers := cfg.MaxWorkers
	if maxWorkers == 0 {
		maxWorkers = defaultGoalMaxWorkers
	}
	runner := workerRunner{w: NewWorker(svc, q)}

	// The dispatcher's enqueuer is injected later via BindRiverClient (the shared
	// client is built by the composition root once both subsystems have
	// contributed their queue + worker). Until then Enqueuer is nil and the
	// dispatcher skips dispatch — the state tests rely on (they drive Worker.Run).
	disp := NewDispatcher(DispatcherConfig{
		Service:   svc,
		Queries:   q,
		TickEvery: cfg.TickEvery,
		LeaseTTL:  cfg.LeaseTTL,
		Logger:    logger.With("subcomponent", "dispatcher"),
	})

	return &Service{
		Queries:         q,
		Goal:            svc,
		Dispatcher:      disp,
		runner:          runner,
		queueMaxWorkers: maxWorkers,
		logger:          logger,
	}, nil
}

// bootExecutor picks the worker executor. With a Chat callback it runs persisted
// agent turns (executor.go); without one it is a noop that fails non-retryably so
// a misconfigured boot is loud, not silent.
func bootExecutor(chat TaskChatFunc, log *slog.Logger, excludedTools []string, access *agentaccess.Service) Executor {
	if chat == nil {
		log.Warn("goal: no Chat wired; worker executor is a noop")
		return noopExecutor{log: log}
	}
	return newWorkerExecutor(chat, log, excludedTools, access)
}

// noopExecutor fails every attempt non-retryably with a clear hint. It is the
// placeholder the boot wiring installs when BootConfig.Chat is nil, so a
// misconfigured boot fails loudly instead of silently dropping work.
type noopExecutor struct{ log *slog.Logger }

func (n noopExecutor) Execute(_ context.Context, req ExecutorRequest) (ExecutorResult, error) {
	n.log.Warn("goal: noop executor invoked",
		"goal_id", req.Goal.ID, "attempt_id", req.Attempt.ID,
		"hint", "wire BootConfig.Chat to the agent service to execute attempts")
	return ExecutorResult{
		Failed:       true,
		FailReason:   "goal executor not wired (noop): wire BootConfig.Chat to the agent service",
		FailureClass: FailureClassEnvironment,
		BlockedBy:    BlockEnvUnavailable,
	}, nil
}

// workerRunner adapts a *Worker to the dispatcher's WorkerRunner interface: the
// dispatcher spawns a worker for a claimed attempt without an actor in hand, so it
// stamps the system worker actor (the dispatcher is the system, not a user).
type workerRunner struct{ w *Worker }

func (r workerRunner) Run(ctx context.Context, goalID, attemptID string) error {
	return r.w.Run(ctx, goalID, attemptID, Actor{Type: ActorWorker})
}
