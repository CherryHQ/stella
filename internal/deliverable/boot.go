package deliverable

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Service is the boot-level bundle the server + CLI bind to. It owns the
// DeliverableService (the single durable-state writer) and the dispatcher, and
// exposes the read+command surface the HTTP handlers call. Every mutating method
// delegates to DeliverableService so the "lifecycle is written only through a
// transition" invariant holds; reads go straight to the querier.
type Service struct {
	Queries     *sqlc.Queries
	Deliverable *DeliverableService
	Dispatcher  *Dispatcher
}

// TaskChatParams is the worker-turn request passed to BootConfig.Chat. It mirrors
// the old tasks.TaskChatParams so the stellad wiring carries over verbatim: the
// callback resolves AgentID to an agent service and runs one persisted turn in
// the deliverable's session. executor.go consumes this type; it is declared here
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
// transcript persists to the deliverable's session and prior turns load as
// history. An unknown agent surfaces as an Err event on the returned channel.
type TaskChatFunc func(ctx context.Context, p TaskChatParams) <-chan agent.Event

// BootConfig is the minimal wiring needed at server start. It mirrors the old
// tasks.BootConfig: a DB handle, the agent ServiceManager for session minting,
// the Chat callback for worker turns, and the dispatcher tunables.
type BootConfig struct {
	DB       *sql.DB
	Services agent.ServiceManager // registry-backed session minting
	Chat     TaskChatFunc         // runs persisted worker turns; nil => noop executor
	// MaxWorkers, TickEvery, LeaseTTL override defaults; zero values use the
	// dispatcher/service defaults.
	MaxWorkers int
	TickEvery  time.Duration
	LeaseTTL   time.Duration
	Logger     *slog.Logger
}

// Boot constructs the deliverable system and returns the bound bundle. The
// dispatcher is built but not started; the caller registers it on a scheduler via
// Dispatcher.Start.
//
// (Named Boot, not New: the package's DeliverableService constructor already owns
// New(db, q, …). This is the bundle/wiring entry the server binds to.)
//
// Wiring: the worker executor (agent-backed when Chat is non-nil, else a noop that
// fails non-retryably) plus the worker + planning session minters are registered
// on the DeliverableService; one Worker drives claimed attempts and the
// Dispatcher schedules the convergence loop over it.
func Boot(cfg BootConfig) *Service {
	q := sqlc.New(cfg.DB)
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "deliverable")
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

	return &Service{Queries: q, Deliverable: svc, Dispatcher: disp}
}

// bootExecutor picks the worker executor. With a Chat callback it runs persisted
// agent turns (executor.go); without one it is a noop that fails non-retryably so
// a misconfigured boot is loud, not silent.
func bootExecutor(chat TaskChatFunc, log *slog.Logger) Executor {
	if chat == nil {
		log.Warn("deliverable: no Chat wired; worker executor is a noop")
		return noopExecutor{log: log}
	}
	return newWorkerExecutor(chat, log)
}

// noopExecutor fails every attempt non-retryably with a clear hint. It is the
// placeholder the boot wiring installs when BootConfig.Chat is nil, so a
// misconfigured boot fails loudly instead of silently dropping work.
type noopExecutor struct{ log *slog.Logger }

func (n noopExecutor) Execute(_ context.Context, req ExecutorRequest) (ExecutorResult, error) {
	n.log.Warn("deliverable: noop executor invoked",
		"deliverable_id", req.Deliverable.ID, "attempt_id", req.Attempt.ID,
		"hint", "wire BootConfig.Chat to the agent service to execute attempts")
	return ExecutorResult{
		Failed:     true,
		FailReason: "deliverable executor not wired (noop): wire BootConfig.Chat to the agent service",
		Retryable:  false,
	}, nil
}

// workerRunner adapts a *Worker to the dispatcher's WorkerRunner interface: the
// dispatcher spawns a worker for a claimed attempt without an actor in hand, so it
// stamps the system worker actor (the dispatcher is the system, not a user).
type workerRunner struct{ w *Worker }

func (r workerRunner) Run(ctx context.Context, deliverableID, attemptID string) error {
	return r.w.Run(ctx, deliverableID, attemptID, Actor{Type: ActorWorker})
}

// ── Read surface (handlers bind to these; all delegate to the querier) ───────

// DeliverableFilter narrows a root-deliverable list. The zero value lists active
// (non-archived) roots across all agents; populated fields AND together. Terminal
// is tri-state: nil = both, false = active only, true = history (terminal) only.
type DeliverableFilter struct {
	AgentID   string
	Lifecycle string
	ProjectID string
	Terminal  *bool
	Q         string
	Archived  bool
}

func (f DeliverableFilter) includeArchived() any {
	if f.Archived {
		return int64(1)
	}
	return nil
}

func (f DeliverableFilter) terminalArg() any {
	if f.Terminal == nil {
		return nil
	}
	if *f.Terminal {
		return int64(1)
	}
	return int64(0)
}

// ListDeliverables lists root deliverables (goals: parent_id IS NULL) for a user,
// narrowed by filter. Empty filter strings match all rows.
func (s *Service) ListDeliverables(ctx context.Context, userID string, filter DeliverableFilter, limit, offset int64) ([]sqlc.AgentDlvDeliverable, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.Queries.ListRootDeliverable(ctx, sqlc.ListRootDeliverableParams{
		UserID:          userID,
		AgentID:         nilIfEmpty(filter.AgentID),
		ProjectID:       nilIfEmpty(filter.ProjectID),
		Lifecycle:       nilIfEmpty(filter.Lifecycle),
		Terminal:        filter.terminalArg(),
		Q:               nilIfEmpty(filter.Q),
		IncludeArchived: filter.includeArchived(),
		Limit:           limit,
		Offset:          offset,
	})
}

// GetDeliverable returns one deliverable, mapping a missing row to ErrNotFound.
func (s *Service) GetDeliverable(ctx context.Context, id string) (sqlc.AgentDlvDeliverable, error) {
	return getDeliverable(ctx, s.Queries, id)
}

// ListChildren lists the direct children of a composite deliverable, in position
// order.
func (s *Service) ListChildren(ctx context.Context, parentID string) ([]sqlc.AgentDlvDeliverable, error) {
	return s.Queries.ListDeliverableChildren(ctx, nullStr(parentID))
}

// ListSubtree lists every deliverable in a tree (the whole root_id family).
func (s *Service) ListSubtree(ctx context.Context, rootID string) ([]sqlc.AgentDlvDeliverable, error) {
	return s.Queries.ListDeliverableByRoot(ctx, rootID)
}

// GetReadiness loads a deliverable + its upstream edges (with upstream lifecycle
// pre-joined) and returns the computed dispatchability view.
func (s *Service) GetReadiness(ctx context.Context, id string) (Readiness, error) {
	d, err := getDeliverable(ctx, s.Queries, id)
	if err != nil {
		return Readiness{}, err
	}
	edges, err := s.Queries.ListEdgeWithUpstreamState(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	return Compute(d, edges, time.Now().UTC()), nil
}

// ListAttempts returns the execution attempts for a deliverable, newest first.
func (s *Service) ListAttempts(ctx context.Context, id string) ([]sqlc.AgentDlvAttempt, error) {
	return s.Queries.ListAttemptByDeliverable(ctx, sqlc.ListAttemptByDeliverableParams{DeliverableID: id})
}

// GetAttempt returns one attempt by id.
func (s *Service) GetAttempt(ctx context.Context, attemptID string) (sqlc.AgentDlvAttempt, error) {
	return s.Queries.GetAttempt(ctx, attemptID)
}

// ListAcceptanceEvents returns the acceptance ledger for a deliverable, in fold
// (seq) order — the audit trail.
func (s *Service) ListAcceptanceEvents(ctx context.Context, id string) ([]sqlc.AgentDlvAcceptanceEvent, error) {
	return s.Queries.ListAcceptanceEventByDeliverable(ctx, id)
}

// ListEdges returns the upstream dependency edges of a deliverable.
func (s *Service) ListEdges(ctx context.Context, id string) ([]sqlc.AgentDlvEdge, error) {
	return s.Queries.ListEdgeByDeliverable(ctx, id)
}

// ListRevisions returns the decomposition revisions of a composite, newest first.
func (s *Service) ListRevisions(ctx context.Context, id string) ([]sqlc.AgentDlvRevision, error) {
	return s.Queries.ListRevisionByDeliverable(ctx, id)
}

// GetRevision returns one revision by id (handlers use it to enforce
// deliverable↔revision parentage before applying a decision).
func (s *Service) GetRevision(ctx context.Context, revisionID string) (sqlc.AgentDlvRevision, error) {
	return s.Queries.GetRevision(ctx, revisionID)
}

// ── Command surface (delegates to DeliverableService — the single writer) ────

// CreateDeliverable mints a root deliverable (a goal) in 'draft', minting its
// worker session first. Children are created by Materialize, not this entry.
func (s *Service) CreateDeliverable(ctx context.Context, in CreateInput) (sqlc.AgentDlvDeliverable, error) {
	return s.Deliverable.CreateRoot(ctx, in)
}

// CountDeliverables returns the total root deliverables matching the same filter
// as ListDeliverables — it drives the list's exact `total` and the
// active/history/archived header badges (three counts varying only terminal/archived).
func (s *Service) CountDeliverables(ctx context.Context, userID string, filter DeliverableFilter) (int64, error) {
	return s.Queries.CountRootDeliverable(ctx, sqlc.CountRootDeliverableParams{
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
func (s *Service) Activate(ctx context.Context, id string) (sqlc.AgentDlvDeliverable, error) {
	return s.Deliverable.Activate(ctx, id)
}

// UpdateDeliverable applies a partial metadata edit (PATCH).
func (s *Service) UpdateDeliverable(ctx context.Context, id string, in UpdateInput) (sqlc.AgentDlvDeliverable, error) {
	return s.Deliverable.UpdateMetadata(ctx, id, in)
}

// Cancel cascades a cancel over the subtree.
func (s *Service) Cancel(ctx context.Context, id, reason string, by Actor) error {
	return s.Deliverable.Cancel(ctx, id, reason, by)
}

// Abandon is the human give-up on a budget-exhausted block.
func (s *Service) Abandon(ctx context.Context, id, reason string, by Actor) error {
	return s.Deliverable.Abandon(ctx, id, reason, by)
}

// Reattempt raises the budget on a blocked(budget_exhausted) deliverable.
func (s *Service) Reattempt(ctx context.Context, id string, by Actor) error {
	return s.Deliverable.Reattempt(ctx, id, by)
}

// Archive soft-flags a terminal deliverable out of default lists.
func (s *Service) Archive(ctx context.Context, id string) error {
	return s.Deliverable.Archive(ctx, id)
}

// Unarchive clears the archived flag.
func (s *Service) Unarchive(ctx context.Context, id string) error {
	return s.Deliverable.Unarchive(ctx, id)
}

// AddEdge inserts an accepted-output dependency between siblings (cycle-checked).
func (s *Service) AddEdge(ctx context.Context, downstreamID, upstreamID, kind, onFailure string) (sqlc.AgentDlvEdge, error) {
	return s.Deliverable.AddEdge(ctx, downstreamID, upstreamID, kind, onFailure)
}

// WaiveEdge waives a hard edge so a blocked(dep) downstream can proceed.
func (s *Service) WaiveEdge(ctx context.Context, downstreamID, upstreamID, reason string, by Actor) error {
	return s.Deliverable.WaiveEdge(ctx, downstreamID, upstreamID, reason, by)
}

// SubmitVerdict appends a human verdict event and re-folds acceptance.
func (s *Service) SubmitVerdict(ctx context.Context, in VerdictInput) error {
	return s.Deliverable.SubmitVerdict(ctx, in)
}

// PutRevision authors/stages a decomposition edit as a new draft revision.
func (s *Service) PutRevision(ctx context.Context, deliverableID string, content DecompositionContent, sourceAttemptID string) (sqlc.AgentDlvRevision, error) {
	return s.Deliverable.CreateRevision(ctx, deliverableID, content, sourceAttemptID)
}

// StartDecomposition begins a composite's decomposition (draft→active), minting a
// decomposition attempt in the planning session.
func (s *Service) StartDecomposition(ctx context.Context, id string) (sqlc.AgentDlvAttempt, error) {
	return s.Deliverable.BeginDecomposition(ctx, id)
}

// AcceptRevision auto-accepts a draft revision (review_policy=none).
func (s *Service) AcceptRevision(ctx context.Context, revisionID string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.Deliverable.Accept(ctx, revisionID, by)
}

// SubmitRevisionReview moves a draft revision into human review.
func (s *Service) SubmitRevisionReview(ctx context.Context, revisionID string) (sqlc.AgentDlvRevision, error) {
	return s.Deliverable.SubmitForReview(ctx, revisionID)
}

// ApproveRevision accepts an in_review revision (human approval).
func (s *Service) ApproveRevision(ctx context.Context, revisionID string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.Deliverable.Approve(ctx, revisionID, by)
}

// RejectRevision rejects an in_review revision (composite stays active; rework).
func (s *Service) RejectRevision(ctx context.Context, revisionID, reason string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.Deliverable.Reject(ctx, revisionID, reason, by)
}

// RequestChangesRevision sends an in_review revision back to draft for edits.
func (s *Service) RequestChangesRevision(ctx context.Context, revisionID, note string, by Actor) (sqlc.AgentDlvRevision, error) {
	return s.Deliverable.RequestChanges(ctx, revisionID, note, by)
}

// MaterializeRevision creates the revision's children + edges in one tx, then
// lists the materialized children. Child sessions are pre-minted OUTSIDE the tx
// (keyed by child.Key) to avoid the SQLite single-writer self-deadlock.
func (s *Service) MaterializeRevision(ctx context.Context, revisionID string) ([]sqlc.AgentDlvDeliverable, error) {
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
	parent, err := getDeliverable(ctx, s.Queries, rev.DeliverableID)
	if err != nil {
		return nil, err
	}
	var content DecompositionContent
	if err := unmarshalJSON(rev.Content, &content); err != nil {
		return nil, fmt.Errorf("deliverable: revision content: %w", err)
	}
	childSessions := make(map[string]string, len(content.Children))
	for _, ch := range content.Children {
		sid, err := s.Deliverable.newSession(ctx, parent.UserID, parent.AgentID, parent.ProjectID.String)
		if err != nil {
			return nil, fmt.Errorf("deliverable: mint child session %q: %w", ch.Key, err)
		}
		childSessions[ch.Key] = sid
	}
	if err := s.Deliverable.withTx(ctx, func(qtx *sqlc.Queries) error {
		return s.Deliverable.Materialize(ctx, qtx, rev, parent, childSessions)
	}); err != nil {
		return nil, err
	}
	return s.Queries.ListDeliverableChildren(ctx, nullStr(parent.ID))
}

// nilIfEmpty returns nil for an empty string so a sqlc narg filter matches all
// rows; otherwise it returns the value to filter on.
func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
