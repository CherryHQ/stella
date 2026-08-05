package goal

import (
	"strings"
	"testing"
	"time"
)

func TestGoalDetailProjectsExistingExecutionState(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	projectID := "project-1"
	activeAttemptID := "attempt-2"
	root := Goal{
		ID: "root", RootID: "root", Title: "Ship feature", Intent: "Feature is accepted",
		Kind: KindComposite, Priority: PriorityUrgent, Lifecycle: LifecycleBlocked,
		BlockReason: BlockNeedsVerdict, AcceptanceState: AcceptancePending,
		ProjectID: &projectID, ActiveAttemptID: &activeAttemptID, AttemptCount: 2,
		UpdatedAt: now,
	}
	children := []Goal{
		{ID: "accepted", RootID: "root", Lifecycle: LifecycleDone, DoneReason: DoneReasonAccepted, UpdatedAt: now},
		{ID: "blocked", RootID: "root", Lifecycle: LifecycleBlocked, BlockReason: BlockBudgetExhausted, UpdatedAt: now},
		{ID: "active", RootID: "root", Lifecycle: LifecycleActive, UpdatedAt: now},
		{ID: "cancelled", RootID: "root", Lifecycle: LifecycleDone, DoneReason: DoneReasonCancelled, UpdatedAt: now},
	}
	attempts := []AttemptSummary{{
		ID: activeAttemptID, Purpose: PurposeReview, AttemptNo: 1, Status: AttemptRunning,
		SessionID: "session-review", UpdatedAt: now,
	}}

	got := goalDetail(root, children, attempts)
	if !got.NeedsAttention || got.BlockReason != BlockNeedsVerdict || got.ProjectID == nil || *got.ProjectID != projectID {
		t.Fatalf("goal state not projected: %#v", got.goalResponse)
	}
	if got.ChildProgress.Total != 4 || got.ChildProgress.Accepted != 1 || got.ChildProgress.Blocked != 1 || got.ChildProgress.Active != 1 || got.ChildProgress.Cancelled != 1 {
		t.Fatalf("child progress=%#v", got.ChildProgress)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].SessionID != "session-review" || got.Attempts[0].Purpose != PurposeReview {
		t.Fatalf("attempts=%#v", got.Attempts)
	}
}

func TestGoalDetailBoundsNewTextProjection(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	large := strings.Repeat("x", maxToolDetailText)
	got := goalDetail(
		Goal{ID: "root", RootID: "root", Intent: large, UpdatedAt: now},
		[]Goal{{ID: "child", RootID: "root", Title: large, UpdatedAt: now}},
		[]AttemptSummary{{ID: "attempt", Error: large, UpdatedAt: now}},
	)
	if len(got.Intent) != maxToolIntentText || len(got.Children[0].Title)+len(got.Attempts[0].Error)+len(got.Intent) > maxToolDetailText {
		t.Fatalf("detail text was not bounded: intent=%d title=%d error=%d", len(got.Intent), len(got.Children[0].Title), len(got.Attempts[0].Error))
	}
}
