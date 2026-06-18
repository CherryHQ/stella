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
	goals, err := f.ListGoals(context.Background(), h.userID, GoalFilter{AgentID: h.agentID}, 10, 0)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	for _, g := range goals {
		if g.ID == gid {
			t.Fatalf("archived goal returned in default list")
		}
	}
	tasks, err := f.ListTasksByUser(context.Background(), h.userID, h.agentID, "", "", false, 10, 0)
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

// Re-archiving is idempotent: a second DELETE must not surface a spurious
// not-found just because archived_at is already set.
func TestArchiveGoal_Idempotent(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("first ArchiveGoal: %v", err)
	}
	if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("second ArchiveGoal should be a no-op, got %v", err)
	}
}

func TestArchiveTask_Idempotent(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	tid := h.createChildTask(t, gid, StatusDone)
	if err := f.ArchiveTask(context.Background(), tid, SystemActor()); err != nil {
		t.Fatalf("first ArchiveTask: %v", err)
	}
	if err := f.ArchiveTask(context.Background(), tid, SystemActor()); err != nil {
		t.Fatalf("second ArchiveTask should be a no-op, got %v", err)
	}
}

// UnarchiveGoal restores the goal and its archived children, and the archived
// filter surfaces the goal for the history/restore view.
func TestUnarchiveGoal_RestoresGoalAndChildren(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	childID := h.createChildTask(t, gid, StatusDone)
	if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("ArchiveGoal: %v", err)
	}
	// Archived filter must surface it; default filter must not.
	archived, err := f.ListGoals(context.Background(), h.userID, GoalFilter{AgentID: h.agentID, Archived: true}, 10, 0)
	if err != nil {
		t.Fatalf("ListGoals(archived): %v", err)
	}
	if len(archived) != 1 || archived[0].ID != gid {
		t.Fatalf("archived filter did not return the archived goal: %+v", archived)
	}
	if err := f.UnarchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("UnarchiveGoal: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.ArchivedAt.Valid {
		t.Fatalf("goal still archived after unarchive")
	}
	if h.getTask(t, childID).ArchivedAt.Valid {
		t.Fatalf("child still archived after unarchive")
	}
	active, err := f.ListGoals(context.Background(), h.userID, GoalFilter{AgentID: h.agentID}, 10, 0)
	if err != nil {
		t.Fatalf("ListGoals(active): %v", err)
	}
	if len(active) != 1 || active[0].ID != gid {
		t.Fatalf("restored goal not in default list: %+v", active)
	}
}

func TestUnarchiveGoal_NotArchived_NoOp(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	if err := f.UnarchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("UnarchiveGoal on non-archived goal should be a no-op, got %v", err)
	}
}

// An archived goal must stay inert: reactivation may not resurrect work that is
// hidden from default lists.
func TestActivateGoal_Archived_Rejects(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDraft, ReviewPolicyNone)
	if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("ArchiveGoal: %v", err)
	}
	err := h.svc.ActivateGoal(context.Background(), gid, SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v want ErrInvalidTransition", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusDraft {
		t.Errorf("goal status=%q want draft (activation rejected)", goal.Status)
	}
}

func TestReopenTask_Archived_Rejects(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	tid := h.createChildTask(t, gid, StatusDone)
	if err := f.ArchiveTask(context.Background(), tid, SystemActor()); err != nil {
		t.Fatalf("ArchiveTask: %v", err)
	}
	err := h.svc.ReopenTask(context.Background(), tid, false, SystemActor())
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v want ErrInvalidTransition", err)
	}
}

// The dispatcher rollup scan must skip archived goals so an archived (but
// recoverable) failed goal is never silently revived to running.
func TestListAgentGoals_ExcludesArchived(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusFailed, ReviewPolicyNone)
	if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("ArchiveGoal: %v", err)
	}
	goals, err := h.q.ListAgentGoals(context.Background(), sqlc.ListAgentGoalsParams{Limit: 100, Offset: 0})
	if err != nil {
		t.Fatalf("ListAgentGoals: %v", err)
	}
	for _, g := range goals {
		if g.ID == gid {
			t.Fatalf("archived failed goal returned in rollup scan")
		}
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

// CR-013: a child the user archived on their own must NOT be resurrected when a
// parent goal is later archived and then unarchived. Only children that the goal
// archive cascade actually hid are restored.
func TestUnarchiveGoal_SkipsIndependentlyArchivedChild(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	solo := h.createChildTask(t, gid, StatusDone)    // user archives this one first
	cascade := h.createChildTask(t, gid, StatusDone) // hidden by the goal archive
	if err := f.ArchiveTask(context.Background(), solo, SystemActor()); err != nil {
		t.Fatalf("ArchiveTask(solo): %v", err)
	}
	if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("ArchiveGoal: %v", err)
	}
	if err := f.UnarchiveGoal(context.Background(), gid, SystemActor()); err != nil {
		t.Fatalf("UnarchiveGoal: %v", err)
	}
	if !h.getTask(t, solo).ArchivedAt.Valid {
		t.Errorf("independently archived child was wrongly restored")
	}
	if h.getTask(t, cascade).ArchivedAt.Valid {
		t.Errorf("cascade-archived child not restored")
	}
}

// CR-015: an archived goal is inert — lifecycle transitions must not mutate it.
func TestArchivedGoal_RejectsLifecycleTransitions(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	archiveDraft := func(t *testing.T) string {
		gid := h.createGoal(t, GoalStatusDraft, ReviewPolicyNone)
		if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
			t.Fatalf("ArchiveGoal: %v", err)
		}
		return gid
	}
	t.Run("complete", func(t *testing.T) {
		if err := h.svc.CompleteGoal(context.Background(), archiveDraft(t), "{}", SystemActor()); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("got %v want ErrInvalidTransition", err)
		}
	})
	t.Run("fail", func(t *testing.T) {
		if err := h.svc.FailGoal(context.Background(), archiveDraft(t), "boom", SystemActor()); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("got %v want ErrInvalidTransition", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		if err := h.svc.CancelGoal(context.Background(), archiveDraft(t), "x", SystemActor()); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("got %v want ErrInvalidTransition", err)
		}
	})
	t.Run("unblock", func(t *testing.T) {
		// failed is archivable and the rollup unblock path; an archived failed
		// goal must not be revived to running.
		gid := h.createGoal(t, GoalStatusFailed, ReviewPolicyNone)
		if err := f.ArchiveGoal(context.Background(), gid, SystemActor()); err != nil {
			t.Fatalf("ArchiveGoal: %v", err)
		}
		if err := h.svc.UnblockGoal(context.Background(), gid, "child recovered", SystemActor()); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("got %v want ErrInvalidTransition", err)
		}
	})
}

// CR-016: a standalone archived task is discoverable via the archived filter and
// restorable via UnarchiveTask, mirroring goals.
func TestUnarchiveTask_RestoresStandaloneTask(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	tid := h.createChildTask(t, gid, StatusDone)
	if err := f.ArchiveTask(context.Background(), tid, SystemActor()); err != nil {
		t.Fatalf("ArchiveTask: %v", err)
	}
	archived, err := f.ListTasksByUser(context.Background(), h.userID, h.agentID, "", "", true, 10, 0)
	if err != nil {
		t.Fatalf("ListTasksByUser(archived): %v", err)
	}
	if len(archived) != 1 || archived[0].ID != tid {
		t.Fatalf("archived filter did not surface the task: %+v", archived)
	}
	if active, _ := f.ListTasksByUser(context.Background(), h.userID, h.agentID, "", "", false, 10, 0); len(active) != 0 {
		t.Fatalf("archived task leaked into active list: %+v", active)
	}
	if err := f.UnarchiveTask(context.Background(), tid, SystemActor()); err != nil {
		t.Fatalf("UnarchiveTask: %v", err)
	}
	if h.getTask(t, tid).ArchivedAt.Valid {
		t.Fatalf("task still archived after unarchive")
	}
	active, err := f.ListTasksByUser(context.Background(), h.userID, h.agentID, "", "", false, 10, 0)
	if err != nil {
		t.Fatalf("ListTasksByUser(active): %v", err)
	}
	if len(active) != 1 || active[0].ID != tid {
		t.Fatalf("restored task not in default list: %+v", active)
	}
}

func TestUnarchiveTask_NotArchived_NoOp(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	gid := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	tid := h.createChildTask(t, gid, StatusDone)
	if err := f.UnarchiveTask(context.Background(), tid, SystemActor()); err != nil {
		t.Fatalf("UnarchiveTask on non-archived task should be a no-op, got %v", err)
	}
}

// CR-014: the Goals page drives filtering/search server-side. Exercise the
// terminal-group filter, the case-insensitive search, and the matching count.
func TestListGoals_ServerSideFilterSearchAndCount(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, testSessionMinter)
	// Two active (non-terminal) and one terminal goal; titles drive the search.
	h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	h.createGoal(t, GoalStatusDraft, ReviewPolicyNone)
	done := h.createGoal(t, GoalStatusDone, ReviewPolicyNone)
	// Lowercase title vs uppercase query proves the match is case-insensitive.
	if _, err := h.db.ExecContext(context.Background(),
		`UPDATE agent_goal SET title = ? WHERE id = ?`, "find the needle", done); err != nil {
		t.Fatalf("rename: %v", err)
	}

	active := false
	terminal := true
	cases := []struct {
		name   string
		filter GoalFilter
		want   int
	}{
		{"active-only", GoalFilter{AgentID: h.agentID, Terminal: &active}, 2},
		{"terminal-only", GoalFilter{AgentID: h.agentID, Terminal: &terminal}, 1},
		{"search-hit", GoalFilter{AgentID: h.agentID, Search: "NEEDLE"}, 1},
		{"search-miss", GoalFilter{AgentID: h.agentID, Search: "no-such-goal"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := f.ListGoals(context.Background(), h.userID, tc.filter, 50, 0)
			if err != nil {
				t.Fatalf("ListGoals: %v", err)
			}
			if len(rows) != tc.want {
				t.Errorf("ListGoals len=%d want %d", len(rows), tc.want)
			}
			n, err := f.CountGoals(context.Background(), h.userID, tc.filter)
			if err != nil {
				t.Fatalf("CountGoals: %v", err)
			}
			if int(n) != tc.want {
				t.Errorf("CountGoals=%d want %d", n, tc.want)
			}
		})
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
