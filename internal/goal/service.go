package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// SessionMinter returns a fresh durable session id for a goal's
// persistent agent session (worker) or a planning session (decomposition). It
// is called OUTSIDE every tx: minting a session opens its own transaction, and
// running it inside the service tx would self-deadlock (the inner write blocks on
// a row the outer tx holds) and pin a pooled connection. Implementations land in
// the integration phase (session.go).
type SessionMinter func(ctx context.Context, userID, agentID, projectID string) (string, error)

// SessionDisposer archives a session that was minted OUTSIDE a tx whose durable
// write then rolled back (a lost race re-checking eligibility under the lock, a
// budget re-check, or a unique-attempt collision). Without it the orphaned hidden
// session lingers forever — no attempt or goal references it, but it persists. It
// is best effort: it runs outside the tx and a failure only logs (the leak is rare
// — the in-flight guards reject the common re-mint before any session is minted —
// and a future orphan sweep can backstop). A nil disposer is a no-op (tests).
// Implementations land in session.go alongside the minters.
type SessionDisposer func(ctx context.Context, userID, agentID, sessionID string) error

// Executor owns agent interaction for one attempt and is PURE with respect to
// durable state: Execute returns the action it wants; the WORKER applies the
// single transition through GoalService. The agent never mutates
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
	Goal             sqlc.AgentGoal
	Attempt          sqlc.AgentGoalAttempt
	Input            AttemptInput
	OnSandboxSession func(sandbox.Session) error
}

// ExecutorResult is the executor's declared outcome for one attempt. Exactly
// one of the submit/fail paths is meaningful; the worker maps it to a single
// service transition. Decomposition attempts carry produced plan content.
type ExecutorResult struct {
	Submitted     bool
	Evidence      AttemptEvidence
	Output        AttemptOutput
	Decomposition *DecompositionContent // purpose=decomposition only
	Verdicts      []ReviewVerdict       // purpose=review only
	Failed        bool
	FailReason    string
	FailureClass  string
	BlockedBy     string
}

// CapabilityProbe reports deployment capabilities that affect contract
// evaluability at write boundaries.
type CapabilityProbe interface {
	CanRunDeterministic() bool
}

type CapabilityProbeFunc func() bool

func (f CapabilityProbeFunc) CanRunDeterministic() bool { return f() }

// ReviewVerdict is one agent reviewer decision for a required authority=agent
// judgment item (contract §10.13). The worker folds each into an
// authority=agent acceptance_event via SubmitReview; the agent never writes
// lifecycle.
type ReviewVerdict struct {
	ItemID    string `json:"item_id"`
	Pass      bool   `json:"pass"`
	Rationale string `json:"rationale"`
}

// CheckRunner runs ONE deterministic acceptance item in the sandbox and returns
// a CheckResult the service folds into an acceptance_event. It is the only
// sandbox-IO in acceptance; it NEVER writes lifecycle. It runs inside the
// worker, after the executor submits, before the durable transition (sandbox
// exec must never run inside a DB tx, which would pin a pooled connection). The implementation lands in the
// integration phase (runner.go).
type CheckRunner interface {
	Run(ctx context.Context, item AcceptanceItem, env CheckEnv, sess sandbox.Session) (CheckResult, error)
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

// GoalService is the ONLY writer of agent_goal_* rows. Every durable
// change is one withTx: load → guard from→to → side-effects → counter bumps →
// append acceptance_event → commit. Callers branch on the typed errors in
// types.go, never on string Contains. Sessions/sandboxes are minted OUTSIDE the
// tx.
type GoalService struct {
	db                 *pgxpool.Pool
	q                  *sqlc.Queries
	newSession         SessionMinter   // worker session (KindTask)
	newPlanningSession SessionMinter   // decomposition session (KindDelegate)
	disposeSession     SessionDisposer // archives a session orphaned by a rolled-back mint
	checks             CheckRunner
	exec               Executor
	capabilities       CapabilityProbe
	clock              func() time.Time
	cfg                Config
}

// New builds a GoalService. The Queries is used for non-transactional
// reads; mutating methods open their own txns via withTx. Collaborators that
// are nil on a given path surface as clear errors rather than panics.
func New(db *pgxpool.Pool, q *sqlc.Queries, opts ...Option) *GoalService {
	s := &GoalService{
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

// Option configures a GoalService at construction.
type Option func(*GoalService)

// WithSessionMinter sets the worker-session minter.
func WithSessionMinter(m SessionMinter) Option {
	return func(s *GoalService) { s.newSession = m }
}

// WithPlanningSessionMinter sets the decomposition planning-session minter.
func WithPlanningSessionMinter(m SessionMinter) Option {
	return func(s *GoalService) { s.newPlanningSession = m }
}

// WithSessionDisposer sets the disposer used to archive sessions orphaned when a
// mint-then-tx flow rolls back. Optional: a nil disposer leaves the (rare) leak in
// place rather than failing the flow.
func WithSessionDisposer(d SessionDisposer) Option {
	return func(s *GoalService) { s.disposeSession = d }
}

// disposeOnRollback archives pre-minted sessions when txErr indicates the tx
// DEFINITELY rolled back, so a lost race / collision does not orphan them. A nil
// err (committed) or an AMBIGUOUS commit failure (errTxCommit — the server may have
// committed, so the session may now be live) is left alone: archiving a live
// session would break the worker that resumes it. The orphan sweep is the backstop
// for the ambiguous case. Use this at every mint-then-tx site.
func (s *GoalService) disposeOnRollback(ctx context.Context, txErr error, userID, agentID string, sessionIDs ...string) {
	if txErr == nil || errors.Is(txErr, errTxCommit) {
		return
	}
	s.disposeOrphanSessions(ctx, userID, agentID, sessionIDs...)
}

// disposeOrphanSessions archives sessions minted before a write that did not take,
// so they are not orphaned. Best effort and nil-safe: empty ids are skipped and a
// disposer error only logs. It detaches from the caller's cancellation — the tx
// often failed BECAUSE ctx was cancelled/timed out, and reusing it would fail the
// cleanup too — keeping ctx values but bounding the cleanup with a short timeout.
func (s *GoalService) disposeOrphanSessions(ctx context.Context, userID, agentID string, sessionIDs ...string) {
	if s.disposeSession == nil {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, sid := range sessionIDs {
		if sid == "" {
			continue
		}
		if err := s.disposeSession(dctx, userID, agentID, sid); err != nil {
			slog.Default().Warn("goal: dispose orphaned session failed",
				"component", "goal/service", "session_id", sid, "err", err)
		}
	}
}

// WithCheckRunner sets the deterministic check runner.
func WithCheckRunner(r CheckRunner) Option { return func(s *GoalService) { s.checks = r } }

// WithExecutor sets the attempt executor.
func WithExecutor(e Executor) Option { return func(s *GoalService) { s.exec = e } }

// WithCapabilityProbe sets the deployment capability probe.
func WithCapabilityProbe(p CapabilityProbe) Option {
	return func(s *GoalService) { s.capabilities = p }
}

// WithConfig overrides the service config (defaults filled for zero fields).
func WithConfig(c Config) Option {
	return func(s *GoalService) {
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
func (s *GoalService) SetClock(c func() time.Time) { s.clock = c }

// now returns the current UTC time in the RFC3339 text format the JSON
// payload columns (e.g. accepted_output) carry.
func (s *GoalService) now() string {
	return s.clock().UTC().Format(time.RFC3339Nano)
}

// nowTime returns the current UTC instant for TIMESTAMPTZ params, anchored to
// the service clock so tests can drive it.
func (s *GoalService) nowTime() time.Time { return s.clock().UTC() }

func (s *GoalService) canRunDeterministic() bool {
	return s.capabilities == nil || s.capabilities.CanRunDeterministic()
}

// nullTime wraps a time as a non-NULL pgtype.Timestamptz for nullable TIMESTAMPTZ params.
func nullTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// newID mints a new row id (uuid string, matching the old package).
func newID() string { return uuid.Must(uuid.NewV7()).String() }

// nullStr wraps a possibly-empty string as pgtype.Text ("" ⇒ NULL).
// withTx runs fn in a single transaction, rolling back on error and committing
// on success. It does not retry — serialization is the caller's concern.
func (s *GoalService) withTx(ctx context.Context, fn func(*sqlc.Queries) error) error {
	return s.withTxRaw(ctx, func(q *sqlc.Queries, _ pgx.Tx) error { return fn(q) })
}

// withTxRaw is withTx but also hands the raw pgx.Tx to fn, so a caller can run a
// non-sqlc write in the same transaction — specifically River's InsertTx, which
// makes the dispatcher's claim and its durable attempt job commit atomically
// (River Phase 2c). Most callers want withTx; reach for this only when an
// external durable write must be all-or-nothing with the goal-state transition.
//
// fn must NOT Begin/Commit/Rollback the handed tx (this function owns its
// lifecycle) and must NOT retain it past return (it is invalid after commit) —
// the tx is an escape hatch for one in-transaction insert, not a general handle.
func (s *GoalService) withTxRaw(ctx context.Context, fn func(*sqlc.Queries, pgx.Tx) error) (err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(s.q.WithTx(tx), tx); err != nil {
		return err
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		// Tag commit failures so callers can tell a DEFINITE rollback (body error /
		// begin error — nothing committed) from an AMBIGUOUS commit (the server may
		// have committed even though the client saw an error). Compensating cleanup
		// that assumes rollback (disposeOnRollback) must skip the ambiguous case.
		err = fmt.Errorf("%w: %w", errTxCommit, cerr)
	}
	return err
}

// errTxCommit tags a withTxRaw error that originated in tx.Commit — an ambiguous
// outcome (the row may be committed on the server). Distinct from a body/begin
// error, which is a definite rollback. See disposeOnRollback.
var errTxCommit = errors.New("commit")

// getGoal loads a row, mapping pgx.ErrNoRows to ErrNotFound.
func getGoal(ctx context.Context, q *sqlc.Queries, id string) (sqlc.AgentGoal, error) {
	d, err := q.GetGoal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.AgentGoal{}, ErrNotFound
	}
	return d, err
}

// appendAcceptanceEvent allocates the next seq for a goal and appends one
// ledger row in the SAME tx. seq = GetMaxAcceptanceSeq+1 (the query returns -1
// when empty so the first event gets seq 0). The event's id/created_at default
// here when blank.
func (s *GoalService) appendAcceptanceEvent(ctx context.Context, q *sqlc.Queries, e sqlc.AppendAcceptanceEventParams) (sqlc.AgentGoalAcceptanceEvent, error) {
	// Serialize the seq read-modify-write for this goal. GetMaxAcceptanceSeq+1
	// races across parallel writers/nodes under Read Committed, and the ledger's
	// (goal_id, seq) index is NOT unique, so a race would silently duplicate seq
	// and make applyAcceptance's ORDER BY seq fold nondeterministic. The lock
	// releases with the surrounding tx.
	if err := q.LockGoalForWrite(ctx, e.GoalID); err != nil {
		return sqlc.AgentGoalAcceptanceEvent{}, fmt.Errorf("lock goal for acceptance append: %w", err)
	}
	maxSeq, err := q.GetMaxAcceptanceSeq(ctx, e.GoalID)
	if err != nil {
		return sqlc.AgentGoalAcceptanceEvent{}, fmt.Errorf("max acceptance seq: %w", err)
	}
	e.Seq = maxSeq + 1
	if e.ID == "" {
		e.ID = newID()
	}
	if len(e.Detail) == 0 {
		e.Detail = emptyJSON
	}
	if e.Authority == "" {
		e.Authority = AuthoritySystem
	}
	row, err := q.AppendAcceptanceEvent(ctx, e)
	if errors.Is(err, pgx.ErrNoRows) {
		// Natural-key conflict (goal, attempt, item, cache_key): the ledger
		// dedups on re-submit, so a duplicate verdict/check is an idempotent no-op,
		// not a 500. The fold re-reads the whole ledger, so the unreturned row is
		// immaterial — both callers discard it.
		return sqlc.AgentGoalAcceptanceEvent{}, nil
	}
	if err != nil {
		return sqlc.AgentGoalAcceptanceEvent{}, err
	}
	if err := s.appendTimelineAcceptanceRecorded(ctx, q, e); err != nil {
		return sqlc.AgentGoalAcceptanceEvent{}, err
	}
	return row, nil
}

// applyAcceptance is the single fold->transition mapper (contract §4.3). It runs
// in ONE tx: load the goal + its full ledger, DeriveAcceptance, and apply exactly
// one lifecycle move. Nothing else writes acceptance_state / accepted_output.
//
//   - passed -> Accept (freeze output, clear active attempt); composite parent
//     rollup is derived from children.
//   - failed + budget left -> write gaps on the attempt; the dispatcher mints the
//     next attempt (rework = next attempt, not a node).
//   - failed + budget out -> blocked(budget_exhausted) or done(failed) per
//     escalation/contract shape.
//   - NeedsVerdict -> blocked(needs_verdict) (human) or mint an agent-review
//     attempt (agent) -- routed by the pending judgment item's authority.
//
// The stale-projection fence (SetGoalAcceptanceState WHERE
// acceptance_seq < new) rejects a fold computed against an out-of-date seq.
//
// applyAcceptance, Claim, and Submit are implemented in converge.go (the
// convergence seam).

// ── Lifecycle transitions (each one withTx) ─────────────────────────────────

// CreateInput is the request to mint a root or child goal.
type CreateInput struct {
	ID        string // optional deterministic id; explicit ids use idempotent insert
	UserID    string
	AgentID   string
	ProjectID string
	ParentID  string // "" ⇒ root
	RootID    string // = id for a root; parent.root_id for a child
	Depth     int64
	Position  int64
	Title     string
	Intent    string
	Kind      string // leaf | composite
	Priority  string // "" ⇒ routine
	Required  bool

	Contract     AcceptanceContract
	Convergence  ConvergencePolicy
	ReviewPolicy string // "" ⇒ none

	Context        json.RawMessage // empty ⇒ "{}"
	DispatchHint   json.RawMessage // empty ⇒ "{}"
	IdempotencyKey string

	WorkflowID      string
	WorkflowVersion int32
}

// CreateGoal inserts a goal in 'draft' (contract §2.1, (none)→draft).
func (s *GoalService) CreateGoal(ctx context.Context, in CreateInput) (sqlc.AgentGoal, error) {
	id := in.ID
	if id == "" {
		id = newID()
	}
	rootID := in.RootID
	if rootID == "" {
		rootID = id // a root's root_id is its own id
	}
	kind := in.Kind
	if kind == "" {
		kind = KindLeaf
	}
	if in.Contract.HasRequiredDeterministicItem() && !s.canRunDeterministic() {
		return sqlc.AgentGoal{}, ErrDeterministicChecksUnsupported
	}
	// A composite produces no executed output, so a deterministic acceptance item
	// would never get a check event and the fold would stall pending forever.
	// Reject it at the write boundary (issue #579 CR-001).
	if kind == KindComposite && in.Contract.HasDeterministicItem() {
		return sqlc.AgentGoal{}, ErrCompositeDeterministicContract
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
	if len(contextJSON) == 0 {
		contextJSON = emptyJSON
	}
	dispatchHint := in.DispatchHint
	if len(dispatchHint) == 0 {
		dispatchHint = emptyJSON
	}
	params := sqlc.CreateGoalParams{
		ID:                 id,
		UserID:             in.UserID,
		AgentID:            in.AgentID,
		ProjectID:          pgnull.Text(in.ProjectID),
		ParentID:           pgnull.Text(in.ParentID),
		RootID:             rootID,
		Depth:              in.Depth,
		Position:           in.Position,
		Title:              in.Title,
		Intent:             in.Intent,
		Kind:               kind,
		Priority:           priority,
		Required:           in.Required,
		AcceptanceContract: marshalJSON(in.Contract),
		// Materialize the effective policy at create time so the persisted row
		// and the create response show real defaults (max_attempts 3, block,
		// depth 4, concurrent 8) instead of a bare zero policy. Runtime callers
		// still Normalize defensively; freezing here also means a later default
		// change never silently alters an existing goal's budget.
		ConvergencePolicy: marshalJSON(in.Convergence.Normalized()),
		ReviewPolicy:      reviewPolicy,
		Lifecycle:         LifecycleDraft,
		Context:           contextJSON,
		DispatchHint:      dispatchHint,
		WorkflowID:        pgnull.Text(in.WorkflowID),
		WorkflowVersion:   pgtype.Int4{Int32: in.WorkflowVersion, Valid: in.WorkflowVersion > 0},
		IdempotencyKey:    pgnull.Text(in.IdempotencyKey),
	}
	var out sqlc.AgentGoal
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		var d sqlc.AgentGoal
		var err error
		if in.ID != "" {
			d, err = q.CreateGoalIfAbsent(ctx, sqlc.CreateGoalIfAbsentParams(params))
		} else {
			d, err = q.CreateGoal(ctx, params)
		}
		if err != nil {
			return fmt.Errorf("create goal: %w", err)
		}
		out = d
		return nil
	})
	return out, err
}

// CreateRoot creates a new root goal in draft. Attempts mint their own sessions;
// goals no longer own a session.
func (s *GoalService) CreateRoot(ctx context.Context, in CreateInput) (sqlc.AgentGoal, error) {
	return s.CreateGoal(ctx, in)
}

// UpdateInput is the mutable metadata of a goal (PATCH). A nil pointer
// leaves that field unchanged; the writer reads current values and overlays the
// provided ones so a partial edit never clobbers untouched columns.
type UpdateInput struct {
	Title        *string
	Intent       *string
	Priority     *string
	ReviewPolicy *string
	Contract     *AcceptanceContract
	Convergence  *ConvergencePolicy
	// By is the editing actor, recorded on the timeline when the edit resolves
	// a contract conflict.
	By Actor
}

// UpdateMetadata applies a partial metadata edit (contract §2 -- metadata is
// mutable; lifecycle is not). It validates the value-sets it touches and returns
// the refreshed row. Lifecycle and projection are untouched.
func (s *GoalService) UpdateMetadata(ctx context.Context, id string, in UpdateInput) (sqlc.AgentGoal, error) {
	if in.Priority != nil && !ValidPriority(*in.Priority) {
		return sqlc.AgentGoal{}, ErrInvalidTransition
	}
	if in.ReviewPolicy != nil && !ValidReviewPolicy(*in.ReviewPolicy) {
		return sqlc.AgentGoal{}, ErrInvalidTransition
	}
	if in.Contract != nil && !in.Contract.Valid() {
		return sqlc.AgentGoal{}, ErrInvalidContract
	}
	if in.Contract != nil && in.Contract.HasRequiredDeterministicItem() && !s.canRunDeterministic() {
		return sqlc.AgentGoal{}, ErrDeterministicChecksUnsupported
	}
	var out sqlc.AgentGoal
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		p := sqlc.UpdateGoalIntentParams{
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
			// Normalize so a partial policy body cannot zero the untouched knobs.
			p.ConvergencePolicy = marshalJSON(in.Convergence.Normalized())
		}
		if err := q.UpdateGoalIntent(ctx, p); err != nil {
			return fmt.Errorf("update goal metadata: %w", err)
		}
		// A contract conflict can live in any of the three inputs the agent folds
		// together — the acceptance contract, the intent, or the convergence policy
		// (e.g. an intent demanding a two-level plan under max_depth 1) — so editing
		// any of them counts as a resolution attempt and re-enters the lifecycle.
		edited := in.Contract != nil || in.Intent != nil || in.Convergence != nil
		if edited && d.Lifecycle == LifecycleBlocked && d.BlockReason == BlockContractConflict {
			// Tell the next planning/execution attempt the definition changed:
			// prior failure reasons in the frozen timeline context describe the OLD
			// contract/intent/policy, and without this note the planner trusts them
			// over the current row (observed: a raised max_depth was ignored because
			// stale attempt reasons kept "confirming" the old depth limit).
			var edits []string
			if in.Intent != nil {
				edits = append(edits, "intent")
			}
			if in.Contract != nil {
				edits = append(edits, "acceptance contract")
			}
			if in.Convergence != nil {
				edits = append(edits, "convergence policy")
			}
			note := fmt.Sprintf(
				"To resolve the contract conflict the user edited this goal's %s. Earlier failure reasons may describe the old definition — re-evaluate against the current goal state, not past attempts.",
				strings.Join(edits, ", "))
			if _, err := s.appendGoalEvent(ctx, q, id, "", GoalEventHumanMessage, HumanMessagePayload{
				Text:          note,
				ResponderType: in.By.Type,
				ResponderID:   in.By.ID,
			}); err != nil {
				return err
			}
			rows, err := s.transitionGoalLifecycle(ctx, q, d, recoveryLifecycle(d), "")
			if err != nil {
				return fmt.Errorf("recover contract conflict: %w", err)
			}
			if rows == 0 {
				return ErrInvalidTransition
			}
		}
		out, err = getGoal(ctx, q, id)
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

// Activate is the plan gate: draft->pending (contract §2.1). A leaf passes when
// its contract has items or is explicitly trivial; a composite passes when its
// plan is materialized. A composite releases its draft children. Returns
// ErrPlanGate when unmet.
func (s *GoalService) Activate(ctx context.Context, id string) (sqlc.AgentGoal, error) {
	var out sqlc.AgentGoal
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleDraft {
			return ErrInvalidTransition
		}
		if !s.planGateSatisfied(d) {
			return ErrPlanGate
		}
		// A leaf goes to pending so the dispatcher can claim it. A composite is never
		// claimed by a worker — it goes straight to active so the rollup (which only
		// scans active composites) fires once its children accept; landing it in
		// pending would strand it there with no pending->active transition (mirrors the
		// draft→active that BeginDecomposition applies).
		parentTarget := LifecyclePending
		if d.Kind == KindComposite {
			parentTarget = LifecycleActive
		}
		rows, err := s.transitionGoalLifecycle(ctx, q, d, parentTarget, "")
		if err != nil {
			return fmt.Errorf("activate: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		// A composite releases its draft children: leaf children -> pending for the
		// dispatcher, composite children stay draft for scanAndDecompose to plan.
		if d.Kind == KindComposite {
			if err := s.releaseChildren(ctx, q, d.ID); err != nil {
				return err
			}
		}
		out, err = getGoal(ctx, q, id)
		return err
	})
	return out, err
}

// planGateSatisfied reports whether a draft goal clears its plan gate
// (contract §2.1). A leaf passes when its contract has items or is explicitly
// trivial (the empty auto-accept degradation is always allowed). A composite
// passes once its plan is materialized (planned_at set).
func (s *GoalService) planGateSatisfied(d sqlc.AgentGoal) bool {
	if d.Kind == KindComposite {
		return d.PlannedAt.Valid
	}
	// Leaf: an authored contract has items; a trivial ({}) contract auto-accepts.
	// Both clear the gate — a leaf is never gated on a non-trivial empty contract
	// because the trivial form is the intended "direct" leaf.
	return true
}

// Claim is the dispatcher's leaf pending->active (contract §2.1). Guards
// active_attempt_id IS NULL, all hard upstream edges accepted-or-waived, an
// executor resolved, and the concurrency cap. Mints a queued execution attempt,
// sets active_attempt_id, bumps attempt_count, consumes dispatch_hint — one tx.
// Returns ErrConcurrencyCap when over budget, ErrInvalidTransition on a race.
// Implemented in converge.go.

// Block moves pending/active -> blocked with a reason (contract §2.1). Used by
// the fold (needs_verdict) and convergence (budget_exhausted/env/contract).
func (s *GoalService) Block(ctx context.Context, id, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		rows, err := s.blockGoal(ctx, q, d, reason)
		if err != nil {
			return fmt.Errorf("block goal: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // not pending/active (raced)
		}
		return nil
	})
}

// recoveryLifecycle is the lifecycle a goal re-enters when a recoverable block
// clears (a human Unblock/Reattempt or an edge waiver). A leaf returns to pending so
// the dispatcher claims it. A composite is never claimed by a worker: an
// un-planned one returns to draft to (re-)decompose, a planned one to active so
// the rollup drives it. Landing a composite in pending would strand it -- there is
// no pending->active transition for composites (mirrors Activate's draft→active).
func recoveryLifecycle(d sqlc.AgentGoal) string {
	if d.Kind != KindComposite {
		return LifecyclePending
	}
	if d.PlannedAt.Valid {
		return LifecycleActive
	}
	return LifecycleDraft
}

// Unblock clears a recoverable block: blocked->pending (leaf) or ->active/draft
// (composite, see recoveryLifecycle) when the condition cleared (contract §2.1).
func (s *GoalService) Unblock(ctx context.Context, id string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleBlocked {
			return ErrInvalidTransition
		}
		rows, err := s.transitionGoalLifecycle(ctx, q, d, recoveryLifecycle(d), "")
		if err != nil {
			return fmt.Errorf("unblock goal: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return nil
	})
}

// Reattempt raises the budget on a blocked(budget_exhausted) goal so the next
// tick mints a fresh attempt. A leaf returns to pending (the dispatcher claims it);
// a composite, whose budget meters DECOMPOSITION attempts, returns to draft so
// scanAndDecompose re-plans it (contract §2.1, see recoveryLifecycle). For
// blocked(planning_invalid), Reattempt restarts planning without raising the
// model planning budget because in-session repairs are metered separately.
func (s *GoalService) Reattempt(ctx context.Context, id string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleBlocked || (d.BlockReason != BlockBudgetExhausted && d.BlockReason != BlockPlanningInvalid) {
			return ErrInvalidTransition
		}
		if d.BlockReason == BlockPlanningInvalid {
			rows, err := s.transitionGoalLifecycle(ctx, q, d, recoveryLifecycle(d), "")
			if err != nil {
				return fmt.Errorf("reattempt planning invalid: %w", err)
			}
			if rows == 0 {
				return ErrInvalidTransition
			}
			return nil
		}
		if err := q.IncrementGoalBudgetBonus(ctx, id); err != nil {
			return fmt.Errorf("raise budget: %w", err)
		}
		rows, err := s.transitionGoalLifecycle(ctx, q, d, recoveryLifecycle(d), "")
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
// done(cancelled) on each non-terminal descendant, cancel in-flight attempts,
// stamp cancelled_at. Parent rollup observes cancelled children by derived tally.
func (s *GoalService) Cancel(ctx context.Context, id, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		root, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if IsTerminalLifecycle(root.Lifecycle) {
			return ErrInvalidTransition
		}
		subtree, err := q.ListGoalSubtree(ctx, id)
		if err != nil {
			return fmt.Errorf("list subtree for cancel: %w", err)
		}
		// Depth-first, deepest-first: the subtree is depth-ascending, so reverse it
		// to cancel children before their parents. Derived rollup observes cancelled
		// children; no stored parent counters are maintained.
		for i := len(subtree) - 1; i >= 0; i-- {
			d := subtree[i]
			if IsTerminalLifecycle(d.Lifecycle) {
				continue
			}
			// Cancel EVERY in-flight attempt for this descendant before flipping it,
			// not just the one active_attempt_id points at: decomposition and review
			// attempts are never pointed there and would otherwise outlive the cancel,
			// to be reaped later against a terminal goal (the reaper's terminal guard
			// is the backstop; this is the root fix).
			atts, err := q.ListInflightAttemptsByGoal(ctx, d.ID)
			if err != nil {
				return fmt.Errorf("list in-flight attempts for cancel: %w", err)
			}
			for _, att := range atts {
				if _, err := s.finalizeAttempt(ctx, q, att, AttemptCancelled, reason, ""); err != nil {
					return fmt.Errorf("cancel in-flight attempt: %w", err)
				}
			}
			if err := s.cancelGoal(ctx, q, d); err != nil {
				return fmt.Errorf("cancel goal: %w", err)
			}
		}
		return nil
	})
}

// Abandon is the human give-up on blocked(budget_exhausted): blocked->done(failed).
func (s *GoalService) Abandon(ctx context.Context, id, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockBudgetExhausted {
			return ErrInvalidTransition
		}
		rows, err := s.transitionGoalLifecycle(ctx, q, d, LifecycleDone, "")
		if err != nil {
			return fmt.Errorf("abandon: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return nil
	})
}

// Archive soft-flags a terminal goal out of default lists.
func (s *GoalService) Archive(ctx context.Context, id string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if !IsTerminalLifecycle(d.Lifecycle) {
			return ErrInvalidTransition // only a terminal goal archives
		}
		return q.ArchiveGoal(ctx, id)
	})
}

// Unarchive clears the archived flag.
func (s *GoalService) Unarchive(ctx context.Context, id string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		if _, err := getGoal(ctx, q, id); err != nil {
			return err
		}
		return q.UnarchiveGoal(ctx, id)
	})
}

// AddEdge inserts an accepted-output dependency between siblings with a DFS
// cycle check (contract §1.3, §6). Returns ErrCycle when the edge would close a
// cycle.
func (s *GoalService) AddEdge(ctx context.Context, downstreamID, upstreamID, kind, onFailure string) (sqlc.AgentGoalEdge, error) {
	if kind == "" {
		kind = EdgeHard
	}
	if onFailure == "" {
		onFailure = OnFailureBlock
	}
	var out sqlc.AgentGoalEdge
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		if downstreamID == upstreamID {
			return ErrCycle
		}
		if _, err := getGoal(ctx, q, downstreamID); err != nil {
			return err
		}
		if _, err := getGoal(ctx, q, upstreamID); err != nil {
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
			GoalID:     downstreamID,
			UpstreamID: upstreamID,
			EdgeKind:   kind,
			OnFailure:  onFailure,
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
func (s *GoalService) dependsOn(ctx context.Context, q *sqlc.Queries, from, target string) (bool, error) {
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
		edges, err := q.ListEdgeByGoal(ctx, node) // node's upstreams (deps)
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

// WaiveEdge waives a hard edge so downstream readiness can proceed.
func (s *GoalService) WaiveEdge(ctx context.Context, downstreamID, upstreamID, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		if _, err := q.GetEdge(ctx, sqlc.GetEdgeParams{
			GoalID:     downstreamID,
			UpstreamID: upstreamID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("get edge: %w", err)
		}
		if err := q.WaiveEdge(ctx, sqlc.WaiveEdgeParams{
			WaivedByUser: pgnull.Text(by.ID),
			WaiverReason: reason,
			GoalID:       downstreamID,
			UpstreamID:   upstreamID,
		}); err != nil {
			return fmt.Errorf("waive edge: %w", err)
		}
		return nil
	})
}

// VerdictInput is a human judgment-as-evidence (contract §4.2). The handler
// appends it as an acceptance_event and re-folds; it never sets acceptance_state.
type VerdictInput struct {
	GoalID         string
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
func (s *GoalService) SubmitVerdict(ctx context.Context, in VerdictInput) error {
	hv := HumanVerdict{
		GoalID:         in.GoalID,
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
		d, err := getGoal(ctx, q, in.GoalID)
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
	return s.applyAcceptance(ctx, in.GoalID)
}
