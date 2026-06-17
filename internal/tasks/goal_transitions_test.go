package tasks

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// createGoal seeds an agent_goal row in the harness's org.
func (h *testHarness) createGoal(t *testing.T, status, policy string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().Format(time.RFC3339Nano)
	if _, err := h.q.CreateAgentGoal(context.Background(), sqlc.CreateAgentGoalParams{
		ID: id, UserID: h.userID, AgentID: h.agentID,
		Title: "g-" + id[:8], Description: "", Status: status, Priority: "routine",
		ReviewPolicy: policy, Context: "{}", Output: "{}",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	return id
}

// createChildTask seeds a task with goal_id set.
func (h *testHarness) createChildTask(t *testing.T, goalID, status string) string {
	t.Helper()
	id := uuid.NewString()
	sessionID := h.createTaskSession(t, "child-"+id[:8])
	now := time.Now().Format(time.RFC3339Nano)
	if _, err := h.db.ExecContext(context.Background(), `
		INSERT INTO agent_task (id, user_id, agent_id, session_id, goal_id, title, status, priority,
		    required, retry_count, max_retries, context, output, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'routine', 1, 0, 3, '{}', '{}', ?, ?)`,
		id, h.userID, h.agentID, sessionID, goalID, "child-"+id[:8], status, now, now,
	); err != nil {
		t.Fatalf("create child: %v", err)
	}
	return id
}

func TestActivateGoal_DraftToRunning_PromotesDraftChildren(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusDraft, ReviewPolicyNone)
	c1 := h.createChildTask(t, gid, StatusDraft)
	c2 := h.createChildTask(t, gid, StatusReady) // already ready; should stay
	if err := h.svc.ActivateGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("ActivateGoal: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusRunning {
		t.Errorf("goal status=%q want running", goal.Status)
	}
	if got := h.getTask(t, c1).Status; got != StatusReady {
		t.Errorf("c1 status=%q want ready", got)
	}
	if got := h.getTask(t, c2).Status; got != StatusReady {
		t.Errorf("c2 status=%q want ready (unchanged)", got)
	}
}

func TestActivateGoal_NotDraft_Rejects(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	err := h.svc.ActivateGoal(context.Background(), gid, SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v want ErrInvalidTransition", err)
	}
}

func TestActivateGoal_NonNonePolicy_Rejected(t *testing.T) {
	h := newHarness(t)
	// Seed a draft goal with an unsupported policy (direct insert bypasses the
	// create-time gate, mimicking a pre-gating row).
	gid := h.createGoal(t, GoalStatusDraft, ReviewPolicyHuman)
	err := h.svc.ActivateGoal(context.Background(), gid, SystemActor())
	if !errors.Is(err, ErrUnsupportedReviewPolicy) {
		t.Fatalf("got %v want ErrUnsupportedReviewPolicy", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusDraft {
		t.Errorf("goal status=%q want draft (activation rejected)", goal.Status)
	}
}

func TestCreateGoal_NonNonePolicy_Rejected(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	for _, policy := range []string{ReviewPolicyAuto, ReviewPolicyAgent, ReviewPolicyHuman} {
		_, err := f.CreateGoal(context.Background(), CreateGoalInput{
			UserID: h.userID, AgentID: h.agentID, Title: "g", ReviewPolicy: policy,
		})
		if !errors.Is(err, ErrUnsupportedReviewPolicy) {
			t.Errorf("policy=%q: got %v want ErrUnsupportedReviewPolicy", policy, err)
		}
	}
}

func TestCreateGoal_NonePolicy_OK(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	g, err := f.CreateGoal(context.Background(), CreateGoalInput{UserID: h.userID, AgentID: h.agentID, Title: "g"})
	if err != nil {
		t.Fatalf("CreateGoal(none): %v", err)
	}
	if g.ReviewPolicy != ReviewPolicyNone {
		t.Errorf("review_policy=%q want none", g.ReviewPolicy)
	}
}

func TestArchiveGoal_TerminalHidesGoalAndChildren(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	childID := h.createChildTask(t, gid, StatusDone)
	if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("ArchiveGoal: %v", err)
	}
	goal, err := h.q.GetAgentGoal(context.Background(), gid)
	if err != nil {
		t.Fatalf("GetAgentGoal: %v", err)
	}
	if !goal.ArchivedAt.Valid {
		t.Fatalf("goal archived_at not set")
	}
	child := h.getTask(t, childID)
	if !child.ArchivedAt.Valid {
		t.Fatalf("child archived_at not set")
	}
	goals, err := f.ListGoals(context.Background(), h.userID, h.agentID, "", "", 10, 0)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	for _, g := range goals {
		if g.ID == gid {
			t.Fatalf("archived goal returned in default list")
		}
	}
	tasks, err := f.ListTasksByUser(context.Background(), h.userID, h.agentID, "", "", 10, 0)
	if err != nil {
		t.Fatalf("ListTasksByUser: %v", err)
	}
	for _, task := range tasks {
		if task.ID == childID {
			t.Fatalf("archived child returned in default task list")
		}
	}
}

func TestArchiveGoal_RunningChildRejected(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	h.createChildTask(t, gid, StatusRunning)
	err := f.ArchiveGoal(context.Background(), gid, SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v want ErrInvalidTransition", err)
	}
}

func TestCompleteGoal_RunningToDone_StampsCompletedAt(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	if err := h.svc.CompleteGoal(context.Background(), gid, `{"ok":true}`, SystemActor()); err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusDone {
		t.Errorf("status=%q want done", goal.Status)
	}
	if !goal.CompletedAt.Valid {
		t.Errorf("completed_at not set")
	}
}

func TestFailGoal_RunningToFailed(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	if err := h.svc.FailGoal(context.Background(), gid, "boom", SystemActor()); err != nil {
		t.Fatalf("FailGoal: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusFailed {
		t.Errorf("status=%q want failed", goal.Status)
	}
}

func TestCancelGoal_CascadesNonTerminalChildren(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	cReady := h.createChildTask(t, gid, StatusReady)
	cDone := h.createChildTask(t, gid, StatusDone) // terminal — should stay
	if err := h.svc.CancelGoal(context.Background(), gid, "user requested", SystemActor()); err != nil {
		t.Fatalf("CancelGoal: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusCancelled {
		t.Errorf("goal status=%q want cancelled", goal.Status)
	}
	if got := h.getTask(t, cReady).Status; got != StatusCancelled {
		t.Errorf("ready child status=%q want cancelled", got)
	}
	if got := h.getTask(t, cDone).Status; got != StatusDone {
		t.Errorf("done child mutated: %q", got)
	}
}

func TestCancelGoal_AlreadyCancelled_Rejects(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusCancelled, ReviewPolicyNone)
	err := h.svc.CancelGoal(context.Background(), gid, "", SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v want ErrInvalidTransition", err)
	}
}

func TestBlockGoal_RunningToBlocked(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	if err := h.svc.BlockGoal(context.Background(), gid, "child blocked", SystemActor()); err != nil {
		t.Fatalf("BlockGoal: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusBlocked {
		t.Errorf("status=%q want blocked", goal.Status)
	}
}

// Sanity: events written by goal transitions list cleanly.
func TestGoalEvents_RecordTransitions(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusDraft, ReviewPolicyNone)
	if err := h.svc.ActivateGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("ActivateGoal: %v", err)
	}
	events, err := h.q.ListAgentTaskEventsByGoal(context.Background(), sql.NullString{String: gid, Valid: true})
	if err != nil {
		t.Fatalf("list goal events: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("no goal events recorded")
	}
}
