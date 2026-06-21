package goal

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Service is the boot-level bundle the server + CLI bind to. It owns the
// GoalService (the single durable-state writer) and the dispatcher, and
// exposes the read+command surface the HTTP handlers call. Every mutating method
// delegates to GoalService so the "lifecycle is written only through a
// transition" invariant holds; reads go straight to the querier.
type Service struct {
	Queries    *sqlc.Queries
	Goal       *GoalService
	Dispatcher *Dispatcher
}

// TaskChatParams is the worker-turn request passed to BootConfig.Chat. It mirrors
// the old tasks.TaskChatParams so the stellad wiring carries over verbatim: the
// callback resolves AgentID to an agent service and runs one persisted turn in
// the goal's session. executor.go consumes this type; it is declared here
// because BootConfig.Chat is its only producer.
type TaskChatParams struct {
	AgentID    string
	UserID     string
	SessionID  string
	ProjectID  string
	Prompt     string
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
	// MaxWorkers, TickEvery, LeaseTTL override defaults; zero values use the
	// dispatcher/service defaults.
	MaxWorkers int
	TickEvery  time.Duration
	LeaseTTL   time.Duration
	Logger     *slog.Logger
}

// Boot constructs the goal system and returns the bound bundle. The
// dispatcher is built but not started; the caller registers it on a scheduler via
// Dispatcher.Start.
//
// (Named Boot, not New: the package's GoalService constructor already owns
// New(db, q, …). This is the bundle/wiring entry the server binds to.)
//
// Wiring: the worker executor (agent-backed when Chat is non-nil, else a noop that
// fails non-retryably) plus the worker + planning session minters are registered
// on the GoalService; one Worker drives claimed attempts and the
// Dispatcher schedules the convergence loop over it.
func Boot(cfg BootConfig) *Service {
	q := sqlc.New(cfg.DB)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "goal")
	}

	svc := New(cfg.DB, q,
		WithExecutor(bootExecutor(cfg.Chat, logger)),
		WithSessionMinter(RegistrySessionMinter(cfg.Services)),
		WithPlanningSessionMinter(RegistryPlanningSessionMinter(cfg.Services)),
		WithConfig(Config{LeaseTTL: cfg.LeaseTTL}),
	)

	disp := NewDispatcher(DispatcherConfig{
		Service:    svc,
		Queries:    q,
		Worker:     workerRunner{w: NewWorker(svc, q)},
		MaxWorkers: cfg.MaxWorkers,
		TickEvery:  cfg.TickEvery,
		LeaseTTL:   cfg.LeaseTTL,
		Logger:     logger.With("subcomponent", "dispatcher"),
	})

	return &Service{Queries: q, Goal: svc, Dispatcher: disp}
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
		AgentID:         nilIfEmpty(filter.AgentID),
		ProjectID:       nilIfEmpty(filter.ProjectID),
		Lifecycle:       nilIfEmpty(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               nilIfEmpty(filter.Q),
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
	return s.Queries.ListGoalChildren(ctx, nullStr(parentID))
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

// ListRevisions returns the decomposition revisions of a composite, newest first.
func (s *Service) ListRevisions(ctx context.Context, id string) ([]sqlc.AgentGoalRevision, error) {
	return s.Queries.ListRevisionByGoal(ctx, id)
}

// GetRevision returns one revision by id (handlers use it to enforce
// goal↔revision parentage before applying a decision).
func (s *Service) GetRevision(ctx context.Context, revisionID string) (sqlc.AgentGoalRevision, error) {
	return s.Queries.GetRevision(ctx, revisionID)
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
		AgentID:         nilIfEmpty(filter.AgentID),
		ProjectID:       nilIfEmpty(filter.ProjectID),
		Lifecycle:       nilIfEmpty(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               nilIfEmpty(filter.Q),
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

// PutRevision authors/stages a decomposition edit as a new draft revision.
func (s *Service) PutRevision(ctx context.Context, goalID string, content DecompositionContent, sourceAttemptID string) (sqlc.AgentGoalRevision, error) {
	return s.Goal.CreateRevision(ctx, goalID, content, sourceAttemptID)
}

// StartDecomposition begins a composite's decomposition (draft→active), minting a
// decomposition attempt in the planning session.
func (s *Service) StartDecomposition(ctx context.Context, id string) (sqlc.AgentGoalAttempt, error) {
	return s.Goal.BeginDecomposition(ctx, id)
}

// AcceptRevision auto-accepts a draft revision (review_policy=none).
func (s *Service) AcceptRevision(ctx context.Context, revisionID string, by Actor) (sqlc.AgentGoalRevision, error) {
	return s.Goal.Accept(ctx, revisionID, by)
}

// SubmitRevisionReview moves a draft revision into human review.
func (s *Service) SubmitRevisionReview(ctx context.Context, revisionID string) (sqlc.AgentGoalRevision, error) {
	return s.Goal.SubmitForReview(ctx, revisionID)
}

// ApproveRevision accepts an in_review revision (human approval).
func (s *Service) ApproveRevision(ctx context.Context, revisionID string, by Actor) (sqlc.AgentGoalRevision, error) {
	return s.Goal.Approve(ctx, revisionID, by)
}

// RejectRevision rejects an in_review revision (composite stays active; rework).
func (s *Service) RejectRevision(ctx context.Context, revisionID, reason string, by Actor) (sqlc.AgentGoalRevision, error) {
	return s.Goal.Reject(ctx, revisionID, reason, by)
}

// RequestChangesRevision sends an in_review revision back to draft for edits.
func (s *Service) RequestChangesRevision(ctx context.Context, revisionID, note string, by Actor) (sqlc.AgentGoalRevision, error) {
	return s.Goal.RequestChanges(ctx, revisionID, note, by)
}

// MaterializeRevision creates the revision's children + edges in one tx, then
// lists the materialized children. Child sessions are pre-minted OUTSIDE the tx
// (keyed by child.Key) to avoid the SQLite single-writer self-deadlock.
func (s *Service) MaterializeRevision(ctx context.Context, revisionID string) ([]sqlc.AgentGoal, error) {
	rev, err := s.Queries.GetRevision(ctx, revisionID)
	if err != nil {
		return nil, err
	}
	if rev.Status != RevisionAccepted {
		// Materialize only an accepted revision. The in-tx fence (MaterializeRevision
		// requires accepted_at) would reject it anyway, but guarding here avoids
		// minting child sessions and opening a tx that can only roll back.
		return nil, ErrInvalidTransition
	}
	parent, err := getGoal(ctx, s.Queries, rev.GoalID)
	if err != nil {
		return nil, err
	}
	var content DecompositionContent
	if err := unmarshalJSON(rev.Content, &content); err != nil {
		return nil, fmt.Errorf("goal: revision content: %w", err)
	}
	childSessions := make(map[string]string, len(content.Children))
	for _, ch := range content.Children {
		sid, err := s.Goal.newSession(ctx, parent.UserID, parent.AgentID, parent.ProjectID.String)
		if err != nil {
			return nil, fmt.Errorf("goal: mint child session %q: %w", ch.Key, err)
		}
		childSessions[ch.Key] = sid
	}
	if err := s.Goal.withTx(ctx, func(qtx *sqlc.Queries) error {
		return s.Goal.Materialize(ctx, qtx, rev, parent, childSessions)
	}); err != nil {
		return nil, err
	}
	return s.Queries.ListGoalChildren(ctx, nullStr(parent.ID))
}

// nilIfEmpty returns an invalid pgtype.Text for an empty string so a sqlc
// narg filter matches all rows; otherwise it returns the value to filter on.
func nilIfEmpty(v string) pgtype.Text {
	return pgtype.Text{String: v, Valid: v != ""}
}
