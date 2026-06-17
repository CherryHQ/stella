package tasks

import (
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Readiness states. dispatchable is the only state on which the dispatcher
// actually claims; the rest are diagnostic.
const (
	ReadinessDispatchable = "dispatchable"
	ReadinessWaitingDeps  = "waiting_deps"
	ReadinessDeferred     = "deferred" // not_before > now
	ReadinessThrottled    = "throttled"
	ReadinessRunning      = "running"
	ReadinessBlocked      = "blocked"
	ReadinessTerminal     = "terminal"
	ReadinessDraft        = "draft"
	ReadinessReviewing    = "reviewing"
	ReadinessUnknown      = "unknown"
)

// Reason describes why a task is in a given readiness state. Surfaced via
// the readiness API so callers can explain "why isn't this running?".
type Reason struct {
	Type       string // dependency_not_done, dep_failed, not_before, etc.
	UpstreamID string // for dep-related reasons
	Detail     string
}

// Readiness is the computed dispatchability view over a task at a moment in
// time. Pure function over (task row, dep edges with upstream status, now).
type Readiness struct {
	State        string
	Dispatchable bool
	Reasons      []Reason
}

// DepEdgeView pairs an edge with the upstream task's current status, plus the
// fields needed to apply D11's rules without further DB hits.
type DepEdgeView struct {
	DepTaskID      string
	Kind           string // DepKindHard | DepKindSoft
	OnFailure      string // OnFailureBlock | OnFailureFail | OnFailureIgnore
	Waived         bool
	UpstreamStatus string
}

// Compute returns the readiness of task given its dep edges (with upstream
// status pre-joined) at time now. It is a pure function: no DB calls, no
// side effects, no clock reads beyond `now`.
//
// The order of checks below mirrors the readiness flowchart in plan.md
// section 04 — first short-circuit on terminal / non-ready statuses, then
// evaluate deps, then deferred/throttle gates.
func Compute(task sqlc.AgentTask, deps []DepEdgeView, now time.Time) Readiness {
	switch task.Status {
	case StatusDraft:
		return Readiness{State: ReadinessDraft, Dispatchable: false}
	case StatusRunning:
		return Readiness{State: ReadinessRunning, Dispatchable: false}
	case StatusBlocked:
		return Readiness{State: ReadinessBlocked, Dispatchable: false}
	case StatusDone, StatusFailed, StatusCancelled:
		return Readiness{State: ReadinessTerminal, Dispatchable: false}
	case StatusReviewing:
		return Readiness{
			State:        ReadinessReviewing,
			Dispatchable: false,
			Reasons:      []Reason{{Type: "awaiting_review"}},
		}
	case StatusReady:
		// fall through
	default:
		return Readiness{
			State:        ReadinessUnknown,
			Dispatchable: false,
			Reasons:      []Reason{{Type: "unknown_status", Detail: task.Status}},
		}
	}

	// not_before gate: schedule a task for the future.
	if task.NotBefore.Valid {
		if t := task.NotBefore.Time.UTC(); now.Before(t) {
			return Readiness{
				State:        ReadinessDeferred,
				Dispatchable: false,
				Reasons:      []Reason{{Type: "not_before", Detail: t.Format(time.RFC3339Nano)}},
			}
		}
	}

	// Dep evaluation. We must inspect every edge so we can report all blocking
	// reasons rather than just the first.
	var reasons []Reason
	depFailed := false
	for _, d := range deps {
		switch d.Kind {
		case DepKindHard:
			switch d.UpstreamStatus {
			case StatusDone:
				// satisfied
			case StatusFailed, StatusCancelled:
				if d.Waived {
					continue
				}
				switch d.OnFailure {
				case OnFailureIgnore:
					// satisfied without action.
				case OnFailureFail:
					// Caller (dispatcher tick) will propagate failure; treat
					// downstream as terminal-bound for now.
					reasons = append(reasons, Reason{
						Type:       "dep_failed_propagate",
						UpstreamID: d.DepTaskID,
						Detail:     d.UpstreamStatus,
					})
					depFailed = true
				case OnFailureBlock:
					reasons = append(reasons, Reason{
						Type:       "dep_failed_block",
						UpstreamID: d.DepTaskID,
						Detail:     d.UpstreamStatus,
					})
					depFailed = true
				default:
					// Unknown policy is treated as block — fail-safe.
					reasons = append(reasons, Reason{
						Type:       "dep_failed_block",
						UpstreamID: d.DepTaskID,
						Detail:     d.UpstreamStatus,
					})
					depFailed = true
				}
			default:
				// Upstream still in progress.
				reasons = append(reasons, Reason{
					Type:       "dependency_not_done",
					UpstreamID: d.DepTaskID,
					Detail:     d.UpstreamStatus,
				})
			}
		case DepKindSoft:
			// Soft deps require any terminal state; on_failure is ignored.
			switch d.UpstreamStatus {
			case StatusDone, StatusFailed, StatusCancelled:
				// satisfied
			default:
				reasons = append(reasons, Reason{
					Type:       "soft_dep_not_settled",
					UpstreamID: d.DepTaskID,
					Detail:     d.UpstreamStatus,
				})
			}
		default:
			// Unknown dep kind: fail-closed so a malformed edge never lets a
			// downstream task dispatch before its upstream settles.
			reasons = append(reasons, Reason{
				Type:       "dependency_not_done",
				UpstreamID: d.DepTaskID,
				Detail:     d.UpstreamStatus,
			})
		}
	}

	if depFailed {
		// The dispatcher's propagate-dep-failures step will turn this into a
		// blocker or a fail transition; from readiness' POV the task is not
		// dispatchable right now.
		return Readiness{State: ReadinessBlocked, Dispatchable: false, Reasons: reasons}
	}
	if len(reasons) > 0 {
		return Readiness{State: ReadinessWaitingDeps, Dispatchable: false, Reasons: reasons}
	}

	// All deps satisfied. The dispatcher applies concurrency caps and
	// executor resolution after this point; both are runtime checks, not
	// state-of-the-task checks, so Compute reports dispatchable.
	return Readiness{State: ReadinessDispatchable, Dispatchable: true}
}
