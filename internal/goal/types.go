// Package goal is the recursive execution core: one entity (the
// Goal) whose completion is DERIVED from an append-only acceptance
// ledger, never asserted. A goal is a root goal (parent_id NULL);
// children are the same shape all the way down. GoalService owns every
// durable write; the acceptance fold owns "done is derived".
//
// The package is flat: foundation types (enums, contract/evidence structs,
// pure folds) live alongside the service and integration surface so they share
// the unexported helpers directly. Information hiding is by unexported
// identifiers, not subpackages.
package goal

import "errors"

// Goal lifecycle (contract §2). The single state machine every
// goal — root or child, leaf or composite — runs through.
const (
	LifecycleDraft   = "draft"
	LifecyclePending = "pending"
	LifecycleActive  = "active"
	LifecycleBlocked = "blocked"
	LifecycleDone    = "done"
)

const (
	DoneReasonAccepted  = "accepted"
	DoneReasonFailed    = "failed"
	DoneReasonCancelled = "cancelled"
)

// Block reasons (meaningful only when lifecycle='blocked'). Precedence on
// concurrent causes: budget_exhausted > needs_plan_approval > needs_verdict.
// needs_plan_approval is a composite's human-review gate on its proposed plan,
// before any child is materialized; it cannot co-occur with dep/needs_verdict
// (those require materialized children / a submitted attempt).
const (
	BlockBudgetExhausted   = "budget_exhausted"
	BlockNeedsPlanApproval = "needs_plan_approval"
	BlockPlanningInvalid   = "planning_invalid"
	BlockNeedsVerdict      = "needs_verdict"
	BlockEnvUnavailable    = "env_unavailable"
	BlockContractConflict  = "contract_conflict"
)

// Acceptance projection — the evaluation RESULT, distinct from lifecycle.
const (
	AcceptancePending = "pending"
	AcceptancePassed  = "passed"
	AcceptanceFailed  = "failed"
)

// Attempt status (contract §2.2).
const (
	AttemptQueued      = "queued"
	AttemptRunning     = "running"
	AttemptSubmitted   = "submitted" // evidence in; acceptance evaluates next
	AttemptInterrupted = "interrupted"
	AttemptFailed      = "failed"
	AttemptCancelled   = "cancelled"
)

// Attempt purpose.
const (
	PurposeExecution     = "execution"
	PurposeDecomposition = "decomposition"
	PurposeReview        = "review"
)

// Failure class records why a failed/interrupted attempt did not produce a
// usable terminal result.
const (
	FailureClassModel       = "model"
	FailureClassEnvironment = "environment"
	FailureClassContract    = "contract"
	FailureClassFlaky       = "flaky"
)

// Goal kind.
const (
	KindLeaf      = "leaf"
	KindComposite = "composite"
)

// Scheduling priority.
const (
	PriorityRoutine = "routine"
	PriorityUrgent  = "urgent"
)

// Edge kind — hard blocks readiness, soft is advisory.
const (
	EdgeHard = "hard"
	EdgeSoft = "soft"
)

// Edge on-failure policy when an upstream fails.
const (
	OnFailureBlock  = "block"
	OnFailureFail   = "fail"
	OnFailureIgnore = "ignore"
)

// Decomposition review gate.
const (
	ReviewNone  = "none"
	ReviewHuman = "human"
)

// Acceptance-contract policy.
const (
	PolicyDetThenJudgment = "deterministic_then_judgment"
	PolicyAll             = "all"
	PolicyAny             = "any"
)

// Contract item kind.
const (
	ItemDeterministic = "deterministic"
	ItemJudgment      = "judgment"
)

// Acceptance outcome for one item.
const (
	ResultPass = "pass"
	ResultFail = "fail"
)

// Verdict authority.
const (
	AuthoritySystem = "system" // deterministic check result
	AuthorityAgent  = "agent"  // reviewer-agent verdict
	AuthorityHuman  = "human"  // human verdict
)

// Convergence escalation on budget exhaustion.
const (
	EscalationBlock   = "block"   // human decides (default)
	EscalationAbandon = "abandon" // auto-terminal
)

// ValidFailureClass reports whether s is a known failure class. Empty is valid
// for non-failure final states such as cancelled attempts.
func ValidFailureClass(s string) bool {
	switch s {
	case "", FailureClassModel, FailureClassEnvironment, FailureClassContract, FailureClassFlaky:
		return true
	}
	return false
}

// ValidKind reports whether s is a known goal kind.
func ValidKind(s string) bool { return s == KindLeaf || s == KindComposite }

// ValidPriority reports whether s is a known scheduling priority.
func ValidPriority(s string) bool { return s == PriorityRoutine || s == PriorityUrgent }

// ValidEdgeKind reports whether s is a known edge kind.
func ValidEdgeKind(s string) bool { return s == EdgeHard || s == EdgeSoft }

// ValidOnFailure reports whether s is a known edge failure policy.
func ValidOnFailure(s string) bool {
	return s == OnFailureBlock || s == OnFailureFail || s == OnFailureIgnore
}

// ValidReviewPolicy reports whether s is a known decomposition review gate.
func ValidReviewPolicy(s string) bool { return s == ReviewNone || s == ReviewHuman }

// ValidContractPolicy reports whether s is a known acceptance-contract policy.
func ValidContractPolicy(s string) bool {
	return s == PolicyDetThenJudgment || s == PolicyAll || s == PolicyAny
}

// ValidItemKind reports whether s is a known contract item kind.
func ValidItemKind(s string) bool { return s == ItemDeterministic || s == ItemJudgment }

// ValidResult reports whether s is a known acceptance outcome.
func ValidResult(s string) bool { return s == ResultPass || s == ResultFail }

// ValidEscalation reports whether s is a known convergence escalation.
func ValidEscalation(s string) bool { return s == EscalationBlock || s == EscalationAbandon }

// IsTerminalLifecycle reports whether the lifecycle admits no further
// scheduling. 'blocked' is recoverable and so is NOT terminal.
func IsTerminalLifecycle(s string) bool {
	return s == LifecycleDone
}

// legalGoalTransitions is the per-kind lifecycle state machine (contract §2.1).
// Shared edges:
//
//	draft -> active|done
//	pending -> active|blocked|done
//	active -> pending|blocked|done|draft
//	blocked -> pending|active|draft|done
//	done -> none
//
// Leaf-only guards reject draft-bound moves after creation; composite-only callers
// use draft for decomposition/replanning.
var legalGoalTransitions = map[string]map[string]map[string]bool{
	KindLeaf: {
		LifecycleDraft:   {LifecyclePending: true, LifecycleDone: true},
		LifecyclePending: {LifecycleActive: true, LifecycleBlocked: true, LifecycleDone: true},
		LifecycleActive:  {LifecyclePending: true, LifecycleBlocked: true, LifecycleDone: true},
		LifecycleBlocked: {LifecyclePending: true, LifecycleActive: true, LifecycleDone: true},
	},
	KindComposite: {
		LifecycleDraft:   {LifecycleActive: true, LifecycleDone: true},
		LifecycleActive:  {LifecycleDraft: true, LifecycleBlocked: true, LifecycleDone: true},
		LifecycleBlocked: {LifecycleDraft: true, LifecycleActive: true, LifecycleDone: true},
	},
}

// LegalLifecycleTransition reports whether moving a goal of the given kind
// from → to is a legal edge of the §2.1 state machine. An unknown kind, from,
// or to is illegal — fail loud, not open.
func LegalLifecycleTransition(kind, from, to string) bool {
	return legalGoalTransitions[kind][from][to]
}

// NeedsAttention is THE canonical needs-you predicate: whether a goal is
// parked on a block only a human action can clear. It used to live in three
// parallel copies (the web UI's goalNeedsYou, the goals list, the inbox SQL)
// that drifted apart twice; now:
//   - the API serves it as the Goal.needs_attention field (goalToAPI) and the
//     web UI reads that field, never re-deriving it;
//   - ListInboxGoals' WHERE clause is the one remaining SQL copy (the filter
//     must run in the DB), pinned equivalent by
//     TestListInboxGoalsMatchesNeedsAttention.
func NeedsAttention(lifecycle, blockReason string) bool {
	_ = blockReason
	return lifecycle == LifecycleBlocked
}

// Actor describes who initiated a transition. Ported from the old package; the
// goal ledger records verdict authority as a first-class column, but the
// service still carries an Actor for audit/attribution on non-acceptance
// transitions.
type Actor struct {
	Type string // one of ActorSystem | ActorUser | ActorAgent | ActorWorker
	ID   string // user_id / agent_id / worker_id depending on Type; empty for system
}

// Actor types.
const (
	ActorSystem = "system"
	ActorUser   = "user"
	ActorAgent  = "agent"
	ActorWorker = "worker"
)

// SystemActor is the dispatcher/system originator convenience.
func SystemActor() Actor { return Actor{Type: ActorSystem} }

// UserActor attributes a transition to a human.
func UserActor(userID string) Actor { return Actor{Type: ActorUser, ID: userID} }

// Typed sentinel errors. Callers branch on these via errors.Is, never on
// string Contains. APOSD "defined out of existence" governs acceptance: an
// unmet contract simply never produces a pass event, so the goal never
// reaches accepted — there is no "is it really done?" error here. These errors
// cover the guard/validation failures the rest of the package raises.
var (
	// ErrInvalidTransition is returned when a transition's from-lifecycle (or
	// attempt from-status) no longer matches the row. Another tick
	// raced this one; the caller may re-fetch and retry.
	ErrInvalidTransition = errors.New("goal: invalid lifecycle transition")

	// ErrNotFound is returned when the target goal/attempt/edge
	// no longer exists.
	ErrNotFound = errors.New("goal: not found")

	// ErrIllegalLifecycleMove is returned when a routing path asks for a
	// transition the §2.1 state machine has no edge for (legalGoalTransitions).
	// Unlike ErrInvalidTransition (a benign CAS race the dispatcher swallows),
	// this is a PROGRAMMING BUG in the caller: it must surface loudly, never be
	// retried into place.
	ErrIllegalLifecycleMove = errors.New("goal: illegal lifecycle transition")

	// ErrPlanGate is returned by Activate when the plan gate is unmet: a leaf
	// with an empty non-trivial contract, or a composite that is not yet planned
	// (planned_at unset) / has no required children.
	ErrPlanGate = errors.New("goal: plan gate not satisfied")

	// ErrBudgetExhausted is informational: convergence reached MaxAttempts. The
	// goal moves to blocked(budget_exhausted) or done(failed)
	// rather than retrying.
	ErrBudgetExhausted = errors.New("goal: convergence budget exhausted")

	// ErrInvalidContract is returned when an AcceptanceContract or
	// ConvergencePolicy fails structural validation at the write boundary.
	ErrInvalidContract = errors.New("goal: invalid acceptance contract")

	// ErrCompositeDeterministicContract is returned when a composite goal carries
	// a deterministic acceptance item. A composite produces no executed output, so
	// the deterministic check has no event source and the fold would stall pending
	// forever. Put deterministic checks on a leaf child, or use judgment items.
	ErrCompositeDeterministicContract = errors.New("goal: composite contract cannot contain deterministic items")

	// ErrDeterministicChecksUnsupported is returned when the current deployment
	// cannot execute required deterministic acceptance checks.
	ErrDeterministicChecksUnsupported = errors.New("goal: deterministic acceptance checks require a sandbox-capable backend")

	// ErrInvalidDecomposition is returned when a composite's DecompositionContent
	// fails validation (no required child, dangling edge key, etc.).
	ErrInvalidDecomposition = errors.New("goal: invalid decomposition")

	// ErrDepthExceeded is returned at materialize when parent.depth+1 > max_depth.
	ErrDepthExceeded = errors.New("goal: decomposition depth exceeded")

	// ErrCycle is returned by AddEdge / decomposition validation when an edge
	// would close a cycle in the sibling dependency graph.
	ErrCycle = errors.New("goal: dependency cycle")

	// ErrConcurrencyCap is returned (or used to skip a candidate) when a Claim
	// would exceed the per-root or per-user in-flight attempt budget.
	ErrConcurrencyCap = errors.New("goal: concurrency cap reached")

	// ErrStaleProjection is returned when a fold computed against a lower seq
	// than the row's current acceptance_seq is rejected (stale-projection fence).
	ErrStaleProjection = errors.New("goal: stale acceptance projection")

	// ErrInvalidEvidence is returned when submitted evidence violates a contract
	// rule — e.g. a non-root goal submits an empty handoff summary. The
	// worker turns this into a retryable protocol miss.
	ErrInvalidEvidence = errors.New("goal: invalid evidence")

	// ErrInvalidVerdict is returned when a human/agent verdict is malformed
	// (unknown result/authority, empty scope_hash where required).
	ErrInvalidVerdict = errors.New("goal: invalid verdict")
)
