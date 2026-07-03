package goal

import (
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Readiness states. dispatchable is the only state the dispatcher claims on;
// the rest are diagnostic (surfaced via the readiness API to explain "why
// isn't this running?").
const (
	ReadinessDispatchable = "dispatchable"
	ReadinessWaitingDeps  = "waiting_deps"
	ReadinessBlocked      = "blocked"
	ReadinessActive       = "active"
	ReadinessTerminal     = "terminal"
	ReadinessDraft        = "draft"
	ReadinessComposite    = "composite" // a composite is driven by rollup, not claimed
	ReadinessUnknown      = "unknown"
)

// Reason explains a readiness state, e.g. an unsatisfied upstream edge.
type Reason struct {
	Type       string // upstream_not_accepted, upstream_failed_block, soft_upstream_pending, ...
	UpstreamID string
	Detail     string
}

// Readiness is the computed dispatchability view of a goal at a moment.
// Pure function over (goal row, edges with upstream state pre-joined,
// now).
type Readiness struct {
	State        string
	Dispatchable bool
	Reasons      []Reason
}

// Compute returns the readiness of a goal given its upstream edges (with
// upstream lifecycle pre-joined) at time now. PURE: no DB calls, no side
// effects, no clock reads beyond now. An edge means an accepted-output
// dependency — a hard edge is satisfied only when the upstream is 'accepted'
// (or the edge is waived); a soft edge is advisory and never blocks.
//
// now is reserved for future deferral gates (not_before equivalents); it is
// accepted now so the signature is stable when those land.
func Compute(d sqlc.AgentGoal, edges []sqlc.ListEdgeWithUpstreamStateRow, now time.Time) Readiness {
	_ = now
	switch d.Lifecycle {
	case LifecycleDraft:
		return Readiness{State: ReadinessDraft}
	case LifecycleActive:
		return Readiness{State: ReadinessActive}
	case LifecycleBlocked:
		return Readiness{State: ReadinessBlocked, Reasons: []Reason{{Type: "blocked", Detail: d.BlockReason}}}
	case LifecycleDone:
		return Readiness{State: ReadinessTerminal}
	case LifecyclePending:
		// fall through to edge evaluation
	default:
		return Readiness{State: ReadinessUnknown, Reasons: []Reason{{Type: "unknown_lifecycle", Detail: d.Lifecycle}}}
	}

	// Only leaves are claimed; a ready composite is gated by rollup, not edges.
	if d.Kind == KindComposite {
		return Readiness{State: ReadinessComposite}
	}

	var reasons []Reason
	depBlocked := false
	for _, e := range edges {
		waived := e.WaivedAt.Valid
		accepted := e.UpstreamLifecycle == LifecycleDone && e.UpstreamDoneReason == DoneReasonAccepted
		switch e.EdgeKind {
		case EdgeHard:
			if accepted || waived {
				continue // satisfied
			}
			if isUpstreamFailed(e.UpstreamLifecycle, e.UpstreamDoneReason) {
				switch e.OnFailure {
				case OnFailureIgnore:
					// satisfied without action
				case OnFailureFail:
					reasons = append(reasons, Reason{Type: "upstream_failed_propagate", UpstreamID: e.UpstreamID, Detail: e.UpstreamLifecycle})
					depBlocked = true
				default: // block (and unknown ⇒ fail-safe block)
					reasons = append(reasons, Reason{Type: "upstream_failed_block", UpstreamID: e.UpstreamID, Detail: e.UpstreamLifecycle})
					depBlocked = true
				}
				continue
			}
			// upstream still in progress
			reasons = append(reasons, Reason{Type: "upstream_not_accepted", UpstreamID: e.UpstreamID, Detail: e.UpstreamLifecycle})
		case EdgeSoft:
			// Advisory: a soft upstream never blocks dispatch. Surface a
			// diagnostic only, do not flip dispatchable.
			if !accepted && !waived && !IsTerminalLifecycle(e.UpstreamLifecycle) {
				reasons = append(reasons, Reason{Type: "soft_upstream_pending", UpstreamID: e.UpstreamID, Detail: e.UpstreamLifecycle})
			}
		default:
			// Unknown edge kind: fail-closed so a malformed edge never lets a
			// downstream dispatch before its upstream settles.
			if !accepted && !waived {
				reasons = append(reasons, Reason{Type: "upstream_not_accepted", UpstreamID: e.UpstreamID, Detail: e.UpstreamLifecycle})
			}
		}
	}

	if depBlocked {
		return Readiness{State: ReadinessBlocked, Reasons: reasons}
	}
	if hasHardWait(reasons) {
		return Readiness{State: ReadinessWaitingDeps, Reasons: reasons}
	}
	// Soft-only diagnostics do not block dispatch.
	return Readiness{State: ReadinessDispatchable, Dispatchable: true, Reasons: reasons}
}

// isUpstreamFailed reports whether an upstream is done-bad, so a hard downstream
// must apply its on_failure policy.
func isUpstreamFailed(lc, doneReason string) bool {
	return lc == LifecycleDone && (doneReason == DoneReasonFailed || doneReason == DoneReasonCancelled)
}

// hasHardWait reports whether any reason represents an unsatisfied hard edge
// (so the goal is waiting, not dispatchable). Soft diagnostics are
// excluded.
func hasHardWait(reasons []Reason) bool {
	for _, r := range reasons {
		if r.Type == "upstream_not_accepted" {
			return true
		}
	}
	return false
}
