package tasks

import "errors"

// Task lifecycle (Slice 1). Slice 2 widens to include StatusReviewing.
const (
	StatusDraft     = "draft"
	StatusReady     = "ready"
	StatusRunning   = "running"
	StatusBlocked   = "blocked"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Run lifecycle.
const (
	RunQueued      = "queued"
	RunRunning     = "running"
	RunCompleted   = "completed"
	RunFailed      = "failed"
	RunCancelled   = "cancelled"
	RunInterrupted = "interrupted"
	RunTimedOut    = "timed_out"
)

// Blocker lifecycle.
const (
	BlockerOpen      = "open"
	BlockerResolved  = "resolved"
	BlockerCancelled = "cancelled"
)

// Blocker kinds.
const (
	BlockerKindUserInput          = "user_input"
	BlockerKindExternalDependency = "external_dependency"
	BlockerKindToolError          = "tool_error"
	BlockerKindPolicyHold         = "policy_hold"
	BlockerKindDepFailure         = "dep_failure"
)

// Dep edge kinds and failure policies.
const (
	DepKindHard = "hard"
	DepKindSoft = "soft"

	OnFailureBlock  = "block"
	OnFailureFail   = "fail"
	OnFailureIgnore = "ignore"
)

// Actor types written to agent_task_event.
const (
	ActorSystem = "system"
	ActorUser   = "user"
	ActorAgent  = "agent"
	ActorWorker = "worker"
)

// Run kinds (Slice 1 ships worker only; Slice 2+ widen).
const (
	RunKindWorker = "worker"
)

// Actor describes who initiated a transition. Slotted into agent_task_event.
type Actor struct {
	Type string // one of ActorSystem | ActorUser | ActorAgent | ActorWorker
	ID   string // user_id / agent_id / worker_id depending on Type; empty for system
}

// SystemActor is a convenience for transitions originating from the dispatcher
// or migration paths.
func SystemActor() Actor { return Actor{Type: ActorSystem} }

// Typed errors that the transition service returns. Callers branch on these,
// never on string Contains.
var (
	// ErrInvalidTransition is returned when a transition's from-status does not
	// match the row's current status. This usually means another tick or
	// transition raced this one; the caller can re-fetch and retry.
	ErrInvalidTransition = errors.New("tasks: invalid status transition")

	// ErrTaskNotFound is returned when the target row no longer exists.
	ErrTaskNotFound = errors.New("tasks: task not found")

	// ErrBlockerNotFound is returned when a referenced blocker row is missing.
	ErrBlockerNotFound = errors.New("tasks: blocker not found")

	// ErrCycle is returned by AddDep when accepting the new edge would close a
	// cycle in the DAG.
	ErrCycle = errors.New("tasks: dependency cycle")

	// ErrRetryBudgetExhausted is returned by Fail when retry_count has reached
	// max_retries; the task is moved to StatusFailed instead of StatusReady.
	// Callers do not need to handle this differently — it is informational.
	ErrRetryBudgetExhausted = errors.New("tasks: retry budget exhausted")

	// ErrDepFailureUnresolved is returned when a hard dep edge's upstream is
	// failed/cancelled with on_failure=block and the edge has no waiver. The
	// caller must waive the edge via WaiveDep before the downstream can
	// progress.
	ErrDepFailureUnresolved = errors.New("tasks: dep failure requires waiver")

	// ErrAlreadyClosed is returned when attempting to resolve a blocker that
	// is not in the 'open' state.
	ErrAlreadyClosed = errors.New("tasks: blocker is not open")
)

// IsTerminalStatus reports whether the task status is terminal (no further
// transitions until reopen).
func IsTerminalStatus(s string) bool {
	switch s {
	case StatusDone, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// IsActiveRunStatus reports whether a run is still claiming an active slot.
func IsActiveRunStatus(s string) bool {
	switch s {
	case RunQueued, RunRunning:
		return true
	}
	return false
}
