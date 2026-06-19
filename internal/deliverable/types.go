// Package deliverable is the recursive execution core: one entity (the
// Deliverable) whose completion is DERIVED from an append-only acceptance
// ledger, never asserted. A goal is a root deliverable (parent_id NULL);
// children are the same shape all the way down. DeliverableService owns every
// durable write; the acceptance fold owns "done is derived".
//
// The package is flat: foundation types (enums, contract/evidence structs,
// pure folds) live alongside the service and integration surface so they share
// the unexported helpers directly. Information hiding is by unexported
// identifiers, not subpackages.
package deliverable

import "errors"

// Deliverable lifecycle (contract §2). The single state machine every
// deliverable — root or child, leaf or composite — runs through.
const (
	LifecycleDraft         = "draft"
	LifecycleReady         = "ready"
	LifecycleActive        = "active"
	LifecycleBlocked       = "blocked"
	LifecycleAccepted      = "accepted"       // terminal-good; accepted_output frozen
	LifecycleRejectedFinal = "rejected_final" // judgment said no, no rework path left
	LifecycleAbandoned     = "abandoned"      // budget exhausted + give-up
	LifecycleCancelled     = "cancelled"
)

// Block reasons (meaningful only when lifecycle='blocked'). Precedence on
// concurrent causes: budget_exhausted > needs_verdict > dep.
const (
	BlockBudgetExhausted = "budget_exhausted"
	BlockNeedsVerdict    = "needs_verdict"
	BlockDep             = "dep"
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

// Revision (decomposition-review) status (contract §2.3).
const (
	RevisionDraft      = "draft"
	RevisionInReview   = "in_review"
	RevisionAccepted   = "accepted"
	RevisionRejected   = "rejected"
	RevisionSuperseded = "superseded"
)

// Deliverable kind.
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

// ValidLifecycle reports whether s is a known deliverable lifecycle.
func ValidLifecycle(s string) bool {
	switch s {
	case LifecycleDraft, LifecycleReady, LifecycleActive, LifecycleBlocked,
		LifecycleAccepted, LifecycleRejectedFinal, LifecycleAbandoned, LifecycleCancelled:
		return true
	}
	return false
}

// ValidBlockReason reports whether s is a known block reason.
func ValidBlockReason(s string) bool {
	switch s {
	case BlockBudgetExhausted, BlockNeedsVerdict, BlockDep:
		return true
	}
	return false
}

// ValidAcceptanceState reports whether s is a known acceptance projection state.
func ValidAcceptanceState(s string) bool {
	switch s {
	case AcceptancePending, AcceptancePassed, AcceptanceFailed:
		return true
	}
	return false
}

// ValidAttemptStatus reports whether s is a known attempt status.
func ValidAttemptStatus(s string) bool {
	switch s {
	case AttemptQueued, AttemptRunning, AttemptSubmitted,
		AttemptInterrupted, AttemptFailed, AttemptCancelled:
		return true
	}
	return false
}

// ValidPurpose reports whether s is a known attempt purpose.
func ValidPurpose(s string) bool {
	switch s {
	case PurposeExecution, PurposeDecomposition, PurposeReview:
		return true
	}
	return false
}

// ValidRevisionStatus reports whether s is a known revision status.
func ValidRevisionStatus(s string) bool {
	switch s {
	case RevisionDraft, RevisionInReview, RevisionAccepted, RevisionRejected, RevisionSuperseded:
		return true
	}
	return false
}

// ValidKind reports whether s is a known deliverable kind.
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

// ValidAuthority reports whether s is a known verdict authority.
func ValidAuthority(s string) bool {
	return s == AuthoritySystem || s == AuthorityAgent || s == AuthorityHuman
}

// ValidEscalation reports whether s is a known convergence escalation.
func ValidEscalation(s string) bool { return s == EscalationBlock || s == EscalationAbandon }

// IsTerminalLifecycle reports whether the lifecycle admits no further
// scheduling. 'blocked' is recoverable and so is NOT terminal.
func IsTerminalLifecycle(s string) bool {
	switch s {
	case LifecycleAccepted, LifecycleRejectedFinal, LifecycleAbandoned, LifecycleCancelled:
		return true
	}
	return false
}

// Actor describes who initiated a transition. Ported from the old package; the
// deliverable ledger records verdict authority as a first-class column, but the
// service still carries an Actor for audit/attribution on non-acceptance
// transitions.
type Actor struct {
	Type string // one of ActorSystem | ActorUser | ActorAgent | ActorWorker | ActorReviewer | ActorPlanner
	ID   string // user_id / agent_id / worker_id depending on Type; empty for system
}

// Actor types.
const (
	ActorSystem   = "system"
	ActorUser     = "user"
	ActorAgent    = "agent"
	ActorWorker   = "worker"
	ActorReviewer = "reviewer"
	ActorPlanner  = "planner"
)

// SystemActor is the dispatcher/system originator convenience.
func SystemActor() Actor { return Actor{Type: ActorSystem} }

// UserActor attributes a transition to a human.
func UserActor(userID string) Actor { return Actor{Type: ActorUser, ID: userID} }

// Typed sentinel errors. Callers branch on these via errors.Is, never on
// string Contains. APOSD "defined out of existence" governs acceptance: an
// unmet contract simply never produces a pass event, so the deliverable never
// reaches accepted — there is no "is it really done?" error here. These errors
// cover the guard/validation failures the rest of the package raises.
var (
	// ErrInvalidTransition is returned when a transition's from-lifecycle (or
	// attempt/revision from-status) no longer matches the row. Another tick
	// raced this one; the caller may re-fetch and retry.
	ErrInvalidTransition = errors.New("deliverable: invalid lifecycle transition")

	// ErrNotFound is returned when the target deliverable/attempt/revision/edge
	// no longer exists.
	ErrNotFound = errors.New("deliverable: not found")

	// ErrPlanGate is returned by Activate when the plan gate is unmet: a leaf
	// with an empty non-trivial contract, or a composite without a materialized
	// revision / no required children.
	ErrPlanGate = errors.New("deliverable: plan gate not satisfied")

	// ErrBudgetExhausted is informational: convergence reached MaxAttempts. The
	// deliverable moves to blocked(budget_exhausted) or abandoned/rejected_final
	// rather than retrying.
	ErrBudgetExhausted = errors.New("deliverable: convergence budget exhausted")

	// ErrInvalidContract is returned when an AcceptanceContract or
	// ConvergencePolicy fails structural validation at the write boundary.
	ErrInvalidContract = errors.New("deliverable: invalid acceptance contract")

	// ErrInvalidDecomposition is returned when a revision's DecompositionContent
	// fails validation (no required child, dangling edge key, etc.).
	ErrInvalidDecomposition = errors.New("deliverable: invalid decomposition")

	// ErrDepthExceeded is returned at materialize when parent.depth+1 > max_depth.
	ErrDepthExceeded = errors.New("deliverable: decomposition depth exceeded")

	// ErrCycle is returned by AddEdge / decomposition validation when an edge
	// would close a cycle in the sibling dependency graph.
	ErrCycle = errors.New("deliverable: dependency cycle")

	// ErrConcurrencyCap is returned (or used to skip a candidate) when a Claim
	// would exceed the per-root or per-user in-flight attempt budget.
	ErrConcurrencyCap = errors.New("deliverable: concurrency cap reached")

	// ErrStaleProjection is returned when a fold computed against a lower seq
	// than the row's current acceptance_seq is rejected (stale-projection fence).
	ErrStaleProjection = errors.New("deliverable: stale acceptance projection")

	// ErrInvalidEvidence is returned when submitted evidence violates a contract
	// rule — e.g. a non-root deliverable submits an empty handoff summary. The
	// worker turns this into a retryable protocol miss.
	ErrInvalidEvidence = errors.New("deliverable: invalid evidence")

	// ErrInvalidVerdict is returned when a human/agent verdict is malformed
	// (unknown result/authority, empty scope_hash where required).
	ErrInvalidVerdict = errors.New("deliverable: invalid verdict")
)
