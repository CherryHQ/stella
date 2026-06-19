package deliverable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SessionMinter returns a fresh durable session id for a deliverable's
// persistent agent session (worker) or a planning session (decomposition). It
// is called OUTSIDE every tx: minting a session opens its own writer, and the
// SQLite single-writer would self-deadlock if it ran inside the service tx
// (the old boot.go documents this). Implementations land in the integration
// phase (session.go).
type SessionMinter func(ctx context.Context, userID, agentID, projectID string) (string, error)

// Executor owns agent interaction for one attempt and is PURE with respect to
// durable state: Execute returns the action it wants; the WORKER applies the
// single transition through DeliverableService. The agent never mutates
// lifecycle/acceptance — the contract's non-negotiable. The concrete Request /
// Result types are declared by the integration phase (executor.go); they are
// referenced here only behind this interface so the core stays decoupled.
type Executor interface {
	Execute(ctx context.Context, req ExecutorRequest) (ExecutorResult, error)
}

// ExecutorRequest / ExecutorResult are forward-declared handles the integration
// phase fleshes out (executor.go). They are kept opaque here so service.go does
// not depend on the agent layer; the worker is the only caller that constructs
// and consumes them.
type ExecutorRequest struct {
	Deliverable sqlc.AgentDlvDeliverable
	Attempt     sqlc.AgentDlvAttempt
	Input       AttemptInput
}

// ExecutorResult is the executor's declared outcome for one attempt. Exactly
// one of the submit/fail paths is meaningful; the worker maps it to a single
// service transition. Decomposition attempts carry a produced revision content.
type ExecutorResult struct {
	Submitted     bool
	Evidence      AttemptEvidence
	Output        AttemptOutput
	Decomposition *DecompositionContent // purpose=decomposition only
	Failed        bool
	FailReason    string
	Retryable     bool
}

// CheckRunner runs ONE deterministic acceptance item in the sandbox and returns
// a CheckResult the service folds into an acceptance_event. It is the only
// sandbox-IO in acceptance; it NEVER writes lifecycle. It runs inside the
// worker, after the executor submits, before the durable transition (sandbox
// exec must never hold the SQLite writer). The implementation lands in the
// integration phase (runner.go).
type CheckRunner interface {
	Run(ctx context.Context, item AcceptanceItem, env CheckEnv) (CheckResult, error)
}

// Config carries the service's tunables. Zero values fall back to package
// defaults at use sites.
type Config struct {
	// LeaseTTL bounds an attempt's lease before the dispatcher may reap it.
	LeaseTTL time.Duration
	// MaxConcurrentPerUser caps in-flight attempts per user (§5, default 16).
	MaxConcurrentPerUser int
	// StdoutLimit truncates captured stdout before it touches event detail.
	StdoutLimit int
}

const (
	defaultLeaseTTL          = 5 * time.Minute
	defaultMaxConcurrentUser = 16
)

// DeliverableService is the ONLY writer of agent_dlv_* rows. Every durable
// change is one withTx: load → guard from→to → side-effects → counter bumps →
// append acceptance_event → commit. Callers branch on the typed errors in
// types.go, never on string Contains. Sessions/sandboxes are minted OUTSIDE the
// tx.
type DeliverableService struct {
	db                 *sql.DB
	q                  *sqlc.Queries
	newSession         SessionMinter // worker session (KindTask)
	newPlanningSession SessionMinter // decomposition session (KindDelegate)
	checks             CheckRunner
	exec               Executor
	clock              func() time.Time
	cfg                Config
}

// New builds a DeliverableService. The Queries is used for non-transactional
// reads; mutating methods open their own txns via withTx. Collaborators that
// are nil on a given path surface as clear errors rather than panics.
func New(db *sql.DB, q *sqlc.Queries, opts ...Option) *DeliverableService {
	s := &DeliverableService{
		db:    db,
		q:     q,
		clock: time.Now,
		cfg: Config{
			LeaseTTL:             defaultLeaseTTL,
			MaxConcurrentPerUser: defaultMaxConcurrentUser,
		},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures a DeliverableService at construction.
type Option func(*DeliverableService)

// WithSessionMinter sets the worker-session minter.
func WithSessionMinter(m SessionMinter) Option {
	return func(s *DeliverableService) { s.newSession = m }
}

// WithPlanningSessionMinter sets the decomposition planning-session minter.
func WithPlanningSessionMinter(m SessionMinter) Option {
	return func(s *DeliverableService) { s.newPlanningSession = m }
}

// WithCheckRunner sets the deterministic check runner.
func WithCheckRunner(r CheckRunner) Option { return func(s *DeliverableService) { s.checks = r } }

// WithExecutor sets the attempt executor.
func WithExecutor(e Executor) Option { return func(s *DeliverableService) { s.exec = e } }

// WithConfig overrides the service config (defaults filled for zero fields).
func WithConfig(c Config) Option {
	return func(s *DeliverableService) {
		if c.LeaseTTL <= 0 {
			c.LeaseTTL = defaultLeaseTTL
		}
		if c.MaxConcurrentPerUser <= 0 {
			c.MaxConcurrentPerUser = defaultMaxConcurrentUser
		}
		s.cfg = c
	}
}

// SetClock overrides the clock for tests.
func (s *DeliverableService) SetClock(c func() time.Time) { s.clock = c }

// now returns the current UTC time in the naive-UTC TEXT format the columns use.
func (s *DeliverableService) now() string {
	return s.clock().UTC().Format(time.RFC3339Nano)
}

// newID mints a new row id (uuid string, matching the old package).
func newID() string { return uuid.NewString() }

// nullStr wraps a possibly-empty string as sql.NullString ("" ⇒ NULL).
func nullStr(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

// withTx runs fn in a single transaction, rolling back on error and committing
// on success. It does not retry — SQLite serialization is the caller's concern.
func (s *DeliverableService) withTx(ctx context.Context, fn func(*sqlc.Queries) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if cerr := tx.Commit(); cerr != nil {
		err = fmt.Errorf("commit: %w", cerr)
	}
	return err
}

// getDeliverable loads a row, mapping sql.ErrNoRows to ErrNotFound.
func getDeliverable(ctx context.Context, q *sqlc.Queries, id string) (sqlc.AgentDlvDeliverable, error) {
	d, err := q.GetDeliverable(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.AgentDlvDeliverable{}, ErrNotFound
	}
	return d, err
}

// appendAcceptanceEvent allocates the next seq for a deliverable and appends one
// ledger row in the SAME tx. seq = GetMaxAcceptanceSeq+1 (the query returns -1
// when empty so the first event gets seq 0). The event's id/created_at default
// here when blank.
func (s *DeliverableService) appendAcceptanceEvent(ctx context.Context, q *sqlc.Queries, e sqlc.AppendAcceptanceEventParams) (sqlc.AgentDlvAcceptanceEvent, error) {
	maxSeq, err := q.GetMaxAcceptanceSeq(ctx, e.DeliverableID)
	if err != nil {
		return sqlc.AgentDlvAcceptanceEvent{}, fmt.Errorf("max acceptance seq: %w", err)
	}
	e.Seq = maxSeq + 1
	if e.ID == "" {
		e.ID = newID()
	}
	if e.Detail == "" {
		e.Detail = emptyJSON
	}
	if e.Authority == "" {
		e.Authority = AuthoritySystem
	}
	row, err := q.AppendAcceptanceEvent(ctx, e)
	if errors.Is(err, sql.ErrNoRows) {
		// Natural-key conflict (deliverable, attempt, item, cache_key): the ledger
		// dedups on re-submit, so a duplicate verdict/check is an idempotent no-op,
		// not a 500. The fold re-reads the whole ledger, so the unreturned row is
		// immaterial — both callers discard it.
		return sqlc.AgentDlvAcceptanceEvent{}, nil
	}
	return row, err
}

// applyAcceptance is the single fold→transition mapper (contract §4.3). It runs
// in ONE tx: load the deliverable + its full ledger, DeriveAcceptance, and
// apply exactly one lifecycle move. Nothing else writes acceptance_state /
// accepted_output / counters.
//
//   - passed  → Accept (freeze output, clear active attempt, bump parent
//     required_accepted, push downstream readiness).
//   - failed + budget left → write gaps on the attempt; the dispatcher mints the
//     next attempt (rework = next attempt, not a node).
//   - failed + budget out → blocked(budget_exhausted) | abandoned |
//     rejected_final per escalation/contract shape.
//   - NeedsVerdict → blocked(needs_verdict) (human) or mint an agent-review
//     attempt (agent) — routed by the pending judgment item's authority.
//
// The stale-projection fence (SetDeliverableAcceptanceState WHERE
// acceptance_seq < new) rejects a fold computed against an out-of-date seq.
//
// applyAcceptance, Claim, and Submit are implemented in converge.go (the
// convergence seam).

// ── Lifecycle transitions (each one withTx) ─────────────────────────────────

// CreateInput is the request to mint a root or child deliverable. The caller
// pre-mints session_id OUTSIDE any tx and passes it here.
type CreateInput struct {
	UserID    string
	AgentID   string
	ProjectID string
	ParentID  string // "" ⇒ root
	RootID    string // = id for a root; parent.root_id for a child
	Depth     int64
	Position  int64
	SessionID string
	Title     string
	Intent    string
	Kind      string // leaf | composite
	Priority  string // "" ⇒ routine
	Required  bool

	Contract     AcceptanceContract
	Convergence  ConvergencePolicy
	ReviewPolicy string // "" ⇒ none

	Context      string // "" ⇒ "{}"
	DispatchHint string // "" ⇒ "{}"
}

// CreateDeliverable inserts a deliverable in 'draft' (contract §2.1, (none)→draft).
func (s *DeliverableService) CreateDeliverable(ctx context.Context, in CreateInput) (sqlc.AgentDlvDeliverable, error) {
	id := newID()
	rootID := in.RootID
	if rootID == "" {
		rootID = id // a root's root_id is its own id
	}
	kind := in.Kind
	if kind == "" {
		kind = KindLeaf
	}
	priority := in.Priority
	if priority == "" {
		priority = PriorityRoutine
	}
	reviewPolicy := in.ReviewPolicy
	if reviewPolicy == "" {
		reviewPolicy = ReviewNone
	}
	contextJSON := in.Context
	if contextJSON == "" {
		contextJSON = emptyJSON
	}
	dispatchHint := in.DispatchHint
	if dispatchHint == "" {
		dispatchHint = emptyJSON
	}
	var required int64
	if in.Required {
		required = 1
	}

	var out sqlc.AgentDlvDeliverable
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := q.CreateDeliverable(ctx, sqlc.CreateDeliverableParams{
			ID:                 id,
			UserID:             in.UserID,
			AgentID:            in.AgentID,
			ProjectID:          nullStr(in.ProjectID),
			ParentID:           nullStr(in.ParentID),
			RootID:             rootID,
			Depth:              in.Depth,
			Position:           in.Position,
			SessionID:          in.SessionID,
			Title:              in.Title,
			Intent:             in.Intent,
			Kind:               kind,
			Priority:           priority,
			Required:           required,
			AcceptanceContract: marshalJSON(in.Contract),
			ConvergencePolicy:  marshalJSON(in.Convergence),
			ReviewPolicy:       reviewPolicy,
			Lifecycle:          LifecycleDraft,
			Context:            contextJSON,
			DispatchHint:       dispatchHint,
		})
		if err != nil {
			return fmt.Errorf("create deliverable: %w", err)
		}
		out = d
		return nil
	})
	return out, err
}

// CreateRoot mints a worker session for a new root deliverable (a goal) and
// creates it in 'draft'. The session is minted OUTSIDE the insert tx to avoid
// the SQLite single-writer self-deadlock (same discipline as Materialize's
// pre-minted child sessions). Child deliverables get their sessions from
// Materialize instead, so this entry is root-only.
func (s *DeliverableService) CreateRoot(ctx context.Context, in CreateInput) (sqlc.AgentDlvDeliverable, error) {
	if in.SessionID == "" {
		if s.newSession == nil {
			return sqlc.AgentDlvDeliverable{}, fmt.Errorf("deliverable: no session minter configured")
		}
		sid, err := s.newSession(ctx, in.UserID, in.AgentID, in.ProjectID)
		if err != nil {
			return sqlc.AgentDlvDeliverable{}, fmt.Errorf("deliverable: mint root session: %w", err)
		}
		in.SessionID = sid
	}
	return s.CreateDeliverable(ctx, in)
}

// UpdateInput is the mutable metadata of a deliverable (PATCH). A nil pointer
// leaves that field unchanged; the writer reads current values and overlays the
// provided ones so a partial edit never clobbers untouched columns.
type UpdateInput struct {
	Title        *string
	Intent       *string
	Priority     *string
	ReviewPolicy *string
	Contract     *AcceptanceContract
	Convergence  *ConvergencePolicy
}

// UpdateMetadata applies a partial metadata edit (contract §2 — metadata is
// mutable; lifecycle is not). It validates the value-sets it touches and returns
// the refreshed row. Lifecycle, counters, and projection are untouched.
func (s *DeliverableService) UpdateMetadata(ctx context.Context, id string, in UpdateInput) (sqlc.AgentDlvDeliverable, error) {
	if in.Priority != nil && !ValidPriority(*in.Priority) {
		return sqlc.AgentDlvDeliverable{}, ErrInvalidTransition
	}
	if in.ReviewPolicy != nil && !ValidReviewPolicy(*in.ReviewPolicy) {
		return sqlc.AgentDlvDeliverable{}, ErrInvalidTransition
	}
	if in.Contract != nil && !in.Contract.Valid() {
		return sqlc.AgentDlvDeliverable{}, ErrInvalidContract
	}
	var out sqlc.AgentDlvDeliverable
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		p := sqlc.UpdateDeliverableIntentParams{
			ID:                 id,
			Title:              orDefault(in.Title, d.Title),
			Intent:             orDefault(in.Intent, d.Intent),
			Priority:           orDefault(in.Priority, d.Priority),
			ReviewPolicy:       orDefault(in.ReviewPolicy, d.ReviewPolicy),
			AcceptanceContract: d.AcceptanceContract,
			ConvergencePolicy:  d.ConvergencePolicy,
		}
		if in.Contract != nil {
			p.AcceptanceContract = marshalJSON(*in.Contract)
		}
		if in.Convergence != nil {
			p.ConvergencePolicy = marshalJSON(*in.Convergence)
		}
		if err := q.UpdateDeliverableIntent(ctx, p); err != nil {
			return fmt.Errorf("update deliverable metadata: %w", err)
		}
		out, err = getDeliverable(ctx, q, id)
		return err
	})
	return out, err
}

// orDefault returns *v when v is non-nil, else the fallback. Keeps UpdateMetadata's
// partial-overlay one line per field.
func orDefault(v *string, fallback string) string {
	if v != nil {
		return *v
	}
	return fallback
}

// Activate is the plan gate: draft→ready (contract §2.1). A leaf passes when its
// contract has items or is explicitly trivial; a composite passes when it has a
// materialized revision and required_total ≥ 1. A composite flips its draft
// children → ready. Returns ErrPlanGate when unmet.
func (s *DeliverableService) Activate(ctx context.Context, id string) (sqlc.AgentDlvDeliverable, error) {
	var out sqlc.AgentDlvDeliverable
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleDraft {
			return ErrInvalidTransition
		}
		if !s.planGateSatisfied(d) {
			return ErrPlanGate
		}
		rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
			ToLifecycle:   LifecycleReady,
			BlockReason:   "",
			ID:            d.ID,
			FromLifecycle: LifecycleDraft,
		})
		if err != nil {
			return fmt.Errorf("activate: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		// A composite flips its draft children → ready so the dispatcher can begin
		// claiming leaves under it.
		if d.Kind == KindComposite {
			children, err := q.ListDeliverableChildren(ctx, nullStr(d.ID))
			if err != nil {
				return fmt.Errorf("list children for activate: %w", err)
			}
			for _, c := range children {
				if c.Lifecycle != LifecycleDraft {
					continue
				}
				if _, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
					ToLifecycle:   LifecycleReady,
					BlockReason:   "",
					ID:            c.ID,
					FromLifecycle: LifecycleDraft,
				}); err != nil {
					return fmt.Errorf("activate child: %w", err)
				}
			}
		}
		out, err = getDeliverable(ctx, q, id)
		return err
	})
	return out, err
}

// planGateSatisfied reports whether a draft deliverable clears its plan gate
// (contract §2.1). A leaf passes when its contract has items or is explicitly
// trivial (the empty auto-accept degradation is always allowed). A composite
// passes only with a materialized revision and required_total ≥ 1.
func (s *DeliverableService) planGateSatisfied(d sqlc.AgentDlvDeliverable) bool {
	if d.Kind == KindComposite {
		return d.AcceptedRevisionID.Valid && d.AcceptedRevisionID.String != "" && d.RequiredTotal >= 1
	}
	// Leaf: an authored contract has items; a trivial ({}) contract auto-accepts.
	// Both clear the gate — a leaf is never gated on a non-trivial empty contract
	// because the trivial form is the intended "direct" leaf.
	return true
}

// Claim is the dispatcher's leaf ready→active (contract §2.1). Guards
// active_attempt_id IS NULL, all hard upstream edges accepted-or-waived, an
// executor resolved, and the concurrency cap. Mints a queued execution attempt,
// sets active_attempt_id, bumps attempt_count, consumes dispatch_hint — one tx.
// Returns ErrConcurrencyCap when over budget, ErrInvalidTransition on a race.
// Implemented in converge.go.

// Block moves ready/active → blocked with a reason (contract §2.1). Used by the
// dispatcher (dep), the fold (needs_verdict), and convergence (budget_exhausted).
func (s *DeliverableService) Block(ctx context.Context, id, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		rows, err := q.BlockDeliverable(ctx, sqlc.BlockDeliverableParams{
			BlockReason: reason,
			ID:          id,
		})
		if err != nil {
			return fmt.Errorf("block deliverable: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // not ready/active (raced)
		}
		// A required child entering blocked bumps the parent's required_blocked so
		// the rollup surfaces the stall.
		return s.bumpParentCounter(ctx, q, d, counterBlockedIncr)
	})
}

// Unblock clears a recoverable block: blocked(dep)→ready when the condition
// cleared (contract §2.1).
func (s *DeliverableService) Unblock(ctx context.Context, id string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockDep {
			return ErrInvalidTransition // only a recoverable dep block clears to ready
		}
		rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
			ToLifecycle:   LifecycleReady,
			BlockReason:   "",
			ID:            id,
			FromLifecycle: LifecycleBlocked,
		})
		if err != nil {
			return fmt.Errorf("unblock deliverable: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return s.bumpParentCounter(ctx, q, d, counterBlockedDecr)
	})
}

// Reattempt raises the budget on a blocked(budget_exhausted) deliverable so the
// next tick mints an attempt: blocked→ready (contract §2.1).
func (s *DeliverableService) Reattempt(ctx context.Context, id string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockBudgetExhausted {
			return ErrInvalidTransition
		}
		// Raise the convergence budget by one attempt so the next tick mints a fresh
		// attempt (attempt_count stays; max_attempts grows past it).
		var pol ConvergencePolicy
		_ = unmarshalJSON(d.ConvergencePolicy, &pol)
		pol = pol.Normalized()
		pol.MaxAttempts = int(d.AttemptCount) + 1
		if err := q.UpdateDeliverableIntent(ctx, sqlc.UpdateDeliverableIntentParams{
			Title:              d.Title,
			Intent:             d.Intent,
			AcceptanceContract: d.AcceptanceContract,
			ConvergencePolicy:  marshalJSON(pol),
			ReviewPolicy:       d.ReviewPolicy,
			Priority:           d.Priority,
			ID:                 id,
		}); err != nil {
			return fmt.Errorf("raise budget: %w", err)
		}
		rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
			ToLifecycle:   LifecycleReady,
			BlockReason:   "",
			ID:            id,
			FromLifecycle: LifecycleBlocked,
		})
		if err != nil {
			return fmt.Errorf("reattempt: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return nil
	})
}

// Cancel cascades cancel over the subtree (contract §6): depth-first set
// cancelled on each non-terminal descendant, reconcile each touched parent's
// required_failed, cancel in-flight attempts, stamp cancelled_at. One tx.
func (s *DeliverableService) Cancel(ctx context.Context, id, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		root, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		if IsTerminalLifecycle(root.Lifecycle) {
			return ErrInvalidTransition
		}
		subtree, err := q.ListDeliverableSubtree(ctx, id)
		if err != nil {
			return fmt.Errorf("list subtree for cancel: %w", err)
		}
		// Depth-first, deepest-first: the subtree is depth-ascending, so reverse it
		// to cancel children before their parents. Each non-terminal descendant that
		// was required and not yet counted in its parent bumps the parent's
		// required_failed in the same tx (children already accepted keep their
		// required_accepted contribution — their parent is itself being cancelled).
		for i := len(subtree) - 1; i >= 0; i-- {
			d := subtree[i]
			if IsTerminalLifecycle(d.Lifecycle) {
				continue
			}
			// Cancel any in-flight attempt for this descendant before flipping it.
			if d.ActiveAttemptID.Valid && d.ActiveAttemptID.String != "" {
				if _, err := q.FinalizeAttempt(ctx, sqlc.FinalizeAttemptParams{
					ToStatus: AttemptCancelled,
					Error:    reason,
					ID:       d.ActiveAttemptID.String,
				}); err != nil {
					return fmt.Errorf("cancel in-flight attempt: %w", err)
				}
			}
			if err := q.CancelDeliverable(ctx, d.ID); err != nil {
				return fmt.Errorf("cancel deliverable: %w", err)
			}
			// A descendant that was blocked had bumped its parent's required_blocked;
			// cancelling it clears that contribution so the incremental counter still
			// matches the reconcile backstop (which counts lifecycle='blocked'
			// children). Applies to the root too — its required_failed is bumped below.
			if d.Lifecycle == LifecycleBlocked {
				if err := s.bumpParentCounter(ctx, q, d, counterBlockedDecr); err != nil {
					return err
				}
			}
			// A required, non-terminal descendant moving to cancelled is a newly
			// failed requirement for its parent (skip the root being cancelled — its
			// own parent counter is bumped below as the cancel's required_failed).
			if d.ID != id {
				if err := s.bumpParentCounter(ctx, q, d, counterFailed); err != nil {
					return err
				}
			}
		}
		// The cancelled root itself bumps ITS parent's required_failed (§2.1 cancel).
		return s.bumpParentCounter(ctx, q, root, counterFailed)
	})
}

// Abandon is the human give-up on a blocked(budget_exhausted): blocked→abandoned
// (terminal), bumping parent required_failed (contract §2.1).
func (s *DeliverableService) Abandon(ctx context.Context, id, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockBudgetExhausted {
			return ErrInvalidTransition
		}
		rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
			ToLifecycle:   LifecycleAbandoned,
			BlockReason:   "",
			ID:            id,
			FromLifecycle: LifecycleBlocked,
		})
		if err != nil {
			return fmt.Errorf("abandon: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return s.bumpParentCounter(ctx, q, d, counterFailed)
	})
}

// Archive soft-flags a terminal deliverable out of default lists.
func (s *DeliverableService) Archive(ctx context.Context, id string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
		if err != nil {
			return err
		}
		if !IsTerminalLifecycle(d.Lifecycle) {
			return ErrInvalidTransition // only a terminal deliverable archives
		}
		return q.ArchiveDeliverable(ctx, id)
	})
}

// Unarchive clears the archived flag.
func (s *DeliverableService) Unarchive(ctx context.Context, id string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		if _, err := getDeliverable(ctx, q, id); err != nil {
			return err
		}
		return q.UnarchiveDeliverable(ctx, id)
	})
}

// AddEdge inserts an accepted-output dependency between siblings with a DFS
// cycle check (contract §1.3, §6). Returns ErrCycle when the edge would close a
// cycle.
func (s *DeliverableService) AddEdge(ctx context.Context, downstreamID, upstreamID, kind, onFailure string) (sqlc.AgentDlvEdge, error) {
	if kind == "" {
		kind = EdgeHard
	}
	if onFailure == "" {
		onFailure = OnFailureBlock
	}
	var out sqlc.AgentDlvEdge
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		if downstreamID == upstreamID {
			return ErrCycle
		}
		if _, err := getDeliverable(ctx, q, downstreamID); err != nil {
			return err
		}
		if _, err := getDeliverable(ctx, q, upstreamID); err != nil {
			return err
		}
		// Cycle check: the new edge declares downstream depends on upstream. It
		// closes a cycle iff upstream already (transitively) depends on downstream.
		// Walk the existing dependency graph from upstream following each node's own
		// upstreams; reaching downstream means the edge would close a cycle.
		cyclic, err := s.dependsOn(ctx, q, upstreamID, downstreamID)
		if err != nil {
			return err
		}
		if cyclic {
			return ErrCycle
		}
		e, err := q.CreateEdge(ctx, sqlc.CreateEdgeParams{
			DeliverableID: downstreamID,
			UpstreamID:    upstreamID,
			EdgeKind:      kind,
			OnFailure:     onFailure,
		})
		if err != nil {
			return fmt.Errorf("create edge: %w", err)
		}
		out = e
		return nil
	})
	return out, err
}

// dependsOn reports whether `from` already transitively depends on `target`
// through existing edges (from's upstreams, their upstreams, …). A DFS over the
// dependency graph the new edge would extend; used to reject cycle-closing edges.
func (s *DeliverableService) dependsOn(ctx context.Context, q *sqlc.Queries, from, target string) (bool, error) {
	seen := map[string]bool{}
	var visit func(node string) (bool, error)
	visit = func(node string) (bool, error) {
		if node == target {
			return true, nil
		}
		if seen[node] {
			return false, nil
		}
		seen[node] = true
		edges, err := q.ListEdgeByDeliverable(ctx, node) // node's upstreams (deps)
		if err != nil {
			return false, fmt.Errorf("list edges for cycle check: %w", err)
		}
		for _, e := range edges {
			hit, err := visit(e.UpstreamID)
			if err != nil {
				return false, err
			}
			if hit {
				return true, nil
			}
		}
		return false, nil
	}
	return visit(from)
}

// WaiveEdge waives a hard edge so a blocked(dep) downstream can proceed
// (contract §2.1, blocked(dep)→ready).
func (s *DeliverableService) WaiveEdge(ctx context.Context, downstreamID, upstreamID, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		if _, err := q.GetEdge(ctx, sqlc.GetEdgeParams{
			DeliverableID: downstreamID,
			UpstreamID:    upstreamID,
		}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("get edge: %w", err)
		}
		if err := q.WaiveEdge(ctx, sqlc.WaiveEdgeParams{
			WaivedByUser:  nullStr(by.ID),
			WaiverReason:  reason,
			DeliverableID: downstreamID,
			UpstreamID:    upstreamID,
		}); err != nil {
			return fmt.Errorf("waive edge: %w", err)
		}
		// A downstream parked on this dep clears to ready now the edge is waived.
		d, err := getDeliverable(ctx, q, downstreamID)
		if err != nil {
			return err
		}
		if d.Lifecycle == LifecycleBlocked && d.BlockReason == BlockDep {
			rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
				ToLifecycle:   LifecycleReady,
				BlockReason:   "",
				ID:            downstreamID,
				FromLifecycle: LifecycleBlocked,
			})
			if err != nil {
				return fmt.Errorf("unblock waived downstream: %w", err)
			}
			if rows == 0 {
				return ErrInvalidTransition
			}
			return s.bumpParentCounter(ctx, q, d, counterBlockedDecr)
		}
		return nil
	})
}

// VerdictInput is a human judgment-as-evidence (contract §4.2). The handler
// appends it as an acceptance_event and re-folds; it never sets acceptance_state.
type VerdictInput struct {
	DeliverableID  string
	ItemID         string
	Result         string // pass | fail
	Rationale      string
	Scope          string
	ScopeHash      string // the accepted-output/artifact hash the verdict covers
	ReviewerUserID string
}

// SubmitVerdict appends a human verdict event and re-runs applyAcceptance
// (contract §2.1 blocked(needs_verdict)→active/accepted). The verdict is
// evidence, never a state write.
func (s *DeliverableService) SubmitVerdict(ctx context.Context, in VerdictInput) error {
	hv := HumanVerdict{
		DeliverableID:  in.DeliverableID,
		ItemID:         in.ItemID,
		Pass:           in.Result == ResultPass,
		Rationale:      in.Rationale,
		Scope:          in.Scope,
		ScopeHash:      in.ScopeHash,
		ReviewerUserID: in.ReviewerUserID,
	}
	if !hv.Valid() || !ValidResult(in.Result) {
		return ErrInvalidVerdict
	}
	// Append the verdict as a human-authored acceptance_event in its own tx; the
	// verdict is evidence, never a state write. The re-fold below applies the one
	// lifecycle move.
	if err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, in.DeliverableID)
		if err != nil {
			return err
		}
		attemptID, _ := s.evaluatedAttempt(ctx, q, d)
		hv.AttemptID = attemptID // the evaluated attempt the verdict judges
		params := HumanVerdictEvent(hv, AcceptanceItem{ID: in.ItemID})
		params.Authority = AuthorityHuman
		_, err = s.appendAcceptanceEvent(ctx, q, params)
		return err
	}); err != nil {
		return err
	}
	// Re-run derivation over the now-complete ledger (own tx).
	return s.applyAcceptance(ctx, in.DeliverableID)
}

// bumpParentCounter applies exactly one ±1 counter bump on a child's parent in
// the SAME tx that transitions the child (contract §6). Bare SQL +1/-1 (DB does
// the arithmetic) so there is no read-modify-write and no lost update; lock
// order is always child-before-parent.
func (s *DeliverableService) bumpParentCounter(ctx context.Context, q *sqlc.Queries, child sqlc.AgentDlvDeliverable, kind counterKind) error {
	// Only a required child contributes to a parent's rollup counters; a root (no
	// parent) and an advisory (non-required) child are no-ops.
	if !child.ParentID.Valid || child.ParentID.String == "" || child.Required != 1 {
		return nil
	}
	parentID := child.ParentID.String
	switch kind {
	case counterAccepted:
		return q.IncrDeliverableRequiredAccepted(ctx, parentID)
	case counterFailed:
		return q.IncrDeliverableRequiredFailed(ctx, parentID)
	case counterBlockedIncr:
		return q.IncrDeliverableRequiredBlocked(ctx, parentID)
	case counterBlockedDecr:
		return q.DecrDeliverableRequiredBlocked(ctx, parentID)
	default:
		return nil
	}
}

// counterKind selects which incremental parent counter a child transition bumps.
type counterKind int

const (
	counterAccepted counterKind = iota
	counterFailed
	counterBlockedIncr
	counterBlockedDecr
)

// reconcileCounters recomputes a composite's required_* from a COUNT scan over
// its direct children (contract §6 backstop). Run ONLY on rollup-stall
// detection, never per event.
func (s *DeliverableService) reconcileCounters(ctx context.Context, parentID string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		return q.ReconcileDeliverableCounters(ctx, parentID)
	})
}
