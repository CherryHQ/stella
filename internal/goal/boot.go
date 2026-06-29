package goal

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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
// and receives that client back as its dispatcher's enqueuer (SetRiverClient).
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

	// river is the shared working client, injected via SetRiverClient. The
	// dispatcher uses it (as its enqueuer) to dispatch claimed attempts;
	// StartDispatchTick uses it to register the single-leader convergence tick.
	river *river.Client[pgx.Tx]
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

// SetRiverClient injects the shared working River client: it becomes the
// dispatcher's enqueuer and the target StartDispatchTick registers the periodic
// against. Call after the client is built and before StartDispatchTick.
func (s *Service) SetRiverClient(c *river.Client[pgx.Tx]) {
	s.river = c
	s.Dispatcher.SetEnqueuer(c)
}

// StartDispatchTick registers the convergence tick as a single-leader River
// periodic job, replacing the per-node in-process ticker (River Phase 2b). River
// enqueues a periodic only on the elected leader and ByState uniqueness keeps at
// most one tick live, so the cluster runs a single convergence loop; any node's
// tick worker may run a fired tick. RunOnStart fires an immediate tick on
// (re-)election so convergence resumes promptly after a failover or cold start
// rather than waiting a full interval (the cost is one extra idempotent pass on
// failover). Returns the handle for StopDispatchTick. Requires SetRiverClient first.
func (s *Service) StartDispatchTick() (rivertype.PeriodicJobHandle, error) {
	if s.river == nil {
		return 0, fmt.Errorf("goal: StartDispatchTick before SetRiverClient")
	}
	handle := s.river.PeriodicJobs().Add(river.NewPeriodicJob(
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
	if s.river == nil {
		return
	}
	s.river.PeriodicJobs().Remove(handle)
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
	Decompose  bool
	ExtraTools []tools.Tool
}

// TaskChatFunc runs one worker turn through the agent service layer so the
// transcript persists to the goal's session and prior turns load as
// history. An unknown agent surfaces as an Err event on the returned channel.
type TaskChatFunc func(ctx context.Context, p TaskChatParams) <-chan agent.Event

// BootConfig is the minimal wiring needed at server start. It mirrors the old
// tasks.BootConfig: a DB handle, the agent ServiceManager for session minting,
// the Chat callback for worker turns, and the dispatcher tunables.
type BootConfig struct {
	DB       *pgxpool.Pool
	Services agent.ServiceManager // registry-backed session minting
	Chat     TaskChatFunc         // runs persisted worker turns; nil => noop executor
	// MaxWorkers caps concurrent attempt executions per node on the goal River
	// queue (0 => defaultGoalMaxWorkers). TickEvery/LeaseTTL override the
	// dispatcher/service defaults; zero values use them.
	MaxWorkers int
	TickEvery  time.Duration
	LeaseTTL   time.Duration
	Logger     *slog.Logger
}

// Boot constructs the goal system and returns the bound bundle. The dispatcher is
// built but not ticking; the composition root injects the shared client via
// SetRiverClient and the server registers the single-leader tick via
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
		WithExecutor(bootExecutor(cfg.Chat, logger)),
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

	// The dispatcher's enqueuer is injected later via SetRiverClient (the shared
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
func bootExecutor(chat TaskChatFunc, log *slog.Logger) Executor {
	if chat == nil {
		log.Warn("goal: no Chat wired; worker executor is a noop")
		return noopExecutor{log: log}
	}
	return newWorkerExecutor(chat, log)
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
		Failed:     true,
		FailReason: "goal executor not wired (noop): wire BootConfig.Chat to the agent service",
		Retryable:  false,
	}, nil
}

// workerRunner adapts a *Worker to the dispatcher's WorkerRunner interface: the
// dispatcher spawns a worker for a claimed attempt without an actor in hand, so it
// stamps the system worker actor (the dispatcher is the system, not a user).
type workerRunner struct{ w *Worker }

func (r workerRunner) Run(ctx context.Context, goalID, attemptID string) error {
	return r.w.Run(ctx, goalID, attemptID, Actor{Type: ActorWorker})
}

// ── Read surface (handlers bind to these; all delegate to the querier) ───────

// GoalFilter narrows a root-goal list. The zero value lists active
// (non-archived) roots across all agents; populated fields AND together. Terminal
// is tri-state: nil = both, false = active only, true = history (terminal) only.
type GoalFilter struct {
	AgentID   string
	Lifecycle string
	ProjectID string
	Terminal  *bool
	Q         string
	Archived  bool
}

func (f GoalFilter) includeArchived() bool {
	return f.Archived
}

func (f GoalFilter) terminalArg() pgtype.Bool {
	if f.Terminal == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *f.Terminal, Valid: true}
}

// ListGoals lists root goals (goals: parent_id IS NULL) for a user,
// narrowed by filter. Empty filter strings match all rows.
func (s *Service) ListGoals(ctx context.Context, userID string, filter GoalFilter, limit, offset int64) ([]sqlc.AgentGoal, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.Queries.ListRootGoal(ctx, sqlc.ListRootGoalParams{
		UserID:          userID,
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.includeArchived(),
		Limit:           int32(limit),
		Offset:          int32(offset),
	})
}

// GetGoal returns one goal, mapping a missing row to ErrNotFound.
func (s *Service) GetGoal(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	return getGoal(ctx, s.Queries, id)
}

// ListChildren lists the direct children of a composite goal, in position
// order.
func (s *Service) ListChildren(ctx context.Context, parentID string) ([]sqlc.AgentGoal, error) {
	return s.Queries.ListGoalChildren(ctx, pgnull.Text(parentID))
}

// ListSubtree lists every goal in a tree (the whole root_id family).
func (s *Service) ListSubtree(ctx context.Context, rootID string) ([]sqlc.AgentGoal, error) {
	return s.Queries.ListGoalByRoot(ctx, rootID)
}

// GetReadiness loads a goal + its upstream edges (with upstream lifecycle
// pre-joined) and returns the computed dispatchability view.
func (s *Service) GetReadiness(ctx context.Context, id string) (Readiness, error) {
	d, err := getGoal(ctx, s.Queries, id)
	if err != nil {
		return Readiness{}, err
	}
	edges, err := s.Queries.ListEdgeWithUpstreamState(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	return Compute(d, edges, time.Now().UTC()), nil
}

// ListAttempts returns the execution attempts for a goal, newest first.
func (s *Service) ListAttempts(ctx context.Context, id string) ([]sqlc.AgentGoalAttempt, error) {
	return s.Queries.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: id})
}

// GetAttempt returns one attempt by id.
func (s *Service) GetAttempt(ctx context.Context, attemptID string) (sqlc.AgentGoalAttempt, error) {
	return s.Queries.GetAttempt(ctx, attemptID)
}

// ListAcceptanceEvents returns the acceptance ledger for a goal, in fold
// (seq) order — the audit trail.
func (s *Service) ListAcceptanceEvents(ctx context.Context, id string) ([]sqlc.AgentGoalAcceptanceEvent, error) {
	return s.Queries.ListAcceptanceEventByGoal(ctx, id)
}

// ListEdges returns the upstream dependency edges of a goal.
func (s *Service) ListEdges(ctx context.Context, id string) ([]sqlc.AgentGoalEdge, error) {
	return s.Queries.ListEdgeByGoal(ctx, id)
}

// ── Command surface (delegates to GoalService — the single writer) ────

// CreateGoal mints a root goal (a goal) in 'draft', minting its
// worker session first. Children are created by Materialize, not this entry.
func (s *Service) CreateGoal(ctx context.Context, in CreateInput) (sqlc.AgentGoal, error) {
	return s.Goal.CreateRoot(ctx, in)
}

// CountGoals returns the total root goals matching the same filter
// as ListGoals — it drives the list's exact `total` and the
// active/history/archived header badges (three counts varying only terminal/archived).
func (s *Service) CountGoals(ctx context.Context, userID string, filter GoalFilter) (int64, error) {
	return s.Queries.CountRootGoal(ctx, sqlc.CountRootGoalParams{
		UserID:          userID,
		AgentID:         pgnull.Text(filter.AgentID),
		ProjectID:       pgnull.Text(filter.ProjectID),
		Lifecycle:       pgnull.Text(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               pgnull.Text(filter.Q),
		IncludeArchived: filter.includeArchived(),
	})
}

// Activate runs the plan gate: draft→ready.
func (s *Service) Activate(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	return s.Goal.Activate(ctx, id)
}

// UpdateGoal applies a partial metadata edit (PATCH).
func (s *Service) UpdateGoal(ctx context.Context, id string, in UpdateInput) (sqlc.AgentGoal, error) {
	return s.Goal.UpdateMetadata(ctx, id, in)
}

// Cancel cascades a cancel over the subtree.
func (s *Service) Cancel(ctx context.Context, id, reason string, by Actor) error {
	return s.Goal.Cancel(ctx, id, reason, by)
}

// Abandon is the human give-up on a budget-exhausted block.
func (s *Service) Abandon(ctx context.Context, id, reason string, by Actor) error {
	return s.Goal.Abandon(ctx, id, reason, by)
}

// Reattempt raises the budget on a blocked(budget_exhausted) goal.
func (s *Service) Reattempt(ctx context.Context, id string, by Actor) error {
	return s.Goal.Reattempt(ctx, id, by)
}

// Archive soft-flags a terminal goal out of default lists.
func (s *Service) Archive(ctx context.Context, id string) error {
	return s.Goal.Archive(ctx, id)
}

// Unarchive clears the archived flag.
func (s *Service) Unarchive(ctx context.Context, id string) error {
	return s.Goal.Unarchive(ctx, id)
}

// AddEdge inserts an accepted-output dependency between siblings (cycle-checked).
func (s *Service) AddEdge(ctx context.Context, downstreamID, upstreamID, kind, onFailure string) (sqlc.AgentGoalEdge, error) {
	return s.Goal.AddEdge(ctx, downstreamID, upstreamID, kind, onFailure)
}

// WaiveEdge waives a hard edge so a blocked(dep) downstream can proceed.
func (s *Service) WaiveEdge(ctx context.Context, downstreamID, upstreamID, reason string, by Actor) error {
	return s.Goal.WaiveEdge(ctx, downstreamID, upstreamID, reason, by)
}

// SubmitVerdict appends a human verdict event and re-folds acceptance.
func (s *Service) SubmitVerdict(ctx context.Context, in VerdictInput) error {
	return s.Goal.SubmitVerdict(ctx, in)
}

// ApprovePlan approves a composite's proposed plan (blocked(needs_plan_approval)),
// materializing its children and resuming the tree.
func (s *Service) ApprovePlan(ctx context.Context, goalID string, by Actor) error {
	return s.Goal.ApprovePlan(ctx, goalID, by)
}

// RejectPlan rejects a composite's proposed plan, returning it to draft for the
// dispatcher to re-decompose.
func (s *Service) RejectPlan(ctx context.Context, goalID, reason string, by Actor) error {
	return s.Goal.RejectPlan(ctx, goalID, reason, by)
}

// nilIfEmpty returns an invalid pgtype.Text for an empty string so a sqlc
// narg filter matches all rows; otherwise it returns the value to filter on.
