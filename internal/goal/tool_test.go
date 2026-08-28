package goal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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
	children := make([]Goal, 64)
	for i := range children {
		children[i] = Goal{ID: "child", RootID: "root", Title: large, UpdatedAt: now}
	}
	attempts := make([]AttemptSummary, maxToolAttempts)
	for i := range attempts {
		attempts[i] = AttemptSummary{ID: "attempt", Error: large, Status: AttemptFailed, UpdatedAt: now}
	}
	got := goalDetail(
		Goal{ID: "root", RootID: "root", Intent: large, UpdatedAt: now},
		children,
		attempts,
	)
	var total int
	for _, child := range got.Children {
		total += len(child.Title)
		if child.Title == "" || !child.TitleTruncated {
			t.Fatalf("child title should receive a bounded fair share: %#v", child)
		}
	}
	for _, attempt := range got.Attempts {
		total += len(attempt.Error)
		if attempt.Error == "" || !attempt.ErrorTruncated {
			t.Fatalf("attempt error should remain visible and signal truncation: %#v", attempt)
		}
	}
	total += len(got.Intent)
	if !got.IntentTruncated || len(got.Intent) != maxToolIntentText || total > maxToolDetailText {
		t.Fatalf("detail text was not bounded: total=%d intent=%d", total, len(got.Intent))
	}
}

// goal_list declares workflow_id, and until this split the handler dropped it:
// the filter was built without it, so a filtered list quietly returned every
// goal. The assertion is behavioral on purpose — a schema check would still
// have passed against the broken handler.
func TestGoalListFiltersByWorkflowID(t *testing.T) {
	h := newHarness(t)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), h.userID), h.agentID)

	stamped := h.createRoot(KindLeaf, AcceptanceContract{})
	other := h.createRoot(KindLeaf, AcceptanceContract{})
	workflowID := uuid.NewString()
	if _, err := h.db.Exec(ctx, `
		INSERT INTO agent_workflow (id, owner_kind, user_id, agent_id, name, version)
		VALUES ($1, 'agent', $2, $3, 'wf', 1)`, workflowID, h.userID, h.agentID); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	if err := h.q.StampGoalWorkflow(ctx, sqlc.StampGoalWorkflowParams{
		WorkflowID: workflowID, WorkflowVersion: 1, Ids: []string{stamped.ID},
	}); err != nil {
		t.Fatalf("stamp workflow: %v", err)
	}

	list := func(workflowID string) string {
		t.Helper()
		out, err := NewTool(h.bundle, listActionSpec(t)).Execute(ctx, map[string]any{"workflow_id": workflowID})
		if err != nil {
			t.Fatalf("goal_list workflow_id=%q: %v", workflowID, err)
		}
		return out
	}

	filtered := list(workflowID)
	if !strings.Contains(filtered, stamped.ID) {
		t.Fatalf("goal_list workflow_id dropped the stamped goal: %s", filtered)
	}
	if strings.Contains(filtered, other.ID) {
		t.Fatalf("goal_list workflow_id returned an unrelated goal: %s", filtered)
	}
	if all := list(""); !strings.Contains(all, other.ID) {
		t.Fatalf("goal_list without a filter dropped a goal: %s", all)
	}
}

func listActionSpec(t *testing.T) ActionTool {
	t.Helper()
	for _, spec := range ActionTools() {
		if spec.Action == "list" {
			return spec
		}
	}
	t.Fatal("goal has no list action")
	return ActionTool{}
}
