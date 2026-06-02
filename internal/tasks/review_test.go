package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// setReviewPolicy is a test helper that sets the task's review_policy directly.
func setReviewPolicy(t *testing.T, h *testHarness, taskID, policy string) {
	t.Helper()
	if _, err := h.db.Exec(`UPDATE agent_task SET review_policy = ? WHERE id = ?`, policy, taskID); err != nil {
		t.Fatalf("set review policy: %v", err)
	}
}

func TestReview_NonePolicy_BehavesLikeSubmit(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	if err := h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusDone {
		t.Errorf("status=%q want done (none)", got)
	}
}

func TestReview_AutoPolicy_LogsReview_ThenDone(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	setReviewPolicy(t, h, id, ReviewPolicyAuto)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	if err := h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusDone {
		t.Errorf("status=%q want done (auto)", got)
	}
	rows, _ := h.q.ListAgentReviewsByTask(context.Background(), sqlc.ListAgentReviewsByTaskParams{TaskID: nullable(id), Limit: 100})
	if len(rows) != 1 || rows[0].Status != ReviewApproved {
		t.Errorf("expected 1 approved review, got %+v", rows)
	}
}

func TestReview_HumanPolicy_TaskGoesToReviewing(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	setReviewPolicy(t, h, id, ReviewPolicyHuman)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	if err := h.svc.Submit(context.Background(), id, res.RunID, `{"draft":true}`, SystemActor()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusReviewing {
		t.Errorf("status=%q want reviewing", task.Status)
	}
	if !task.ActiveReviewID.Valid {
		t.Errorf("active_review_id should be set")
	}
	// Approve → done.
	if err := h.svc.ApproveReview(context.Background(), task.ActiveReviewID.String, "lgtm", SystemActor()); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusDone {
		t.Errorf("status=%q want done after approve", got)
	}
}

func TestReview_RequestChanges_WithBudget_ReturnsToReady(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	setReviewPolicy(t, h, id, ReviewPolicyHuman)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	_ = h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor())
	task := h.getTask(t, id)
	if err := h.svc.RequestChanges(context.Background(), task.ActiveReviewID.String, "tighten it", "please", SystemActor()); err != nil {
		t.Fatalf("RequestChanges: %v", err)
	}
	got := h.getTask(t, id)
	if got.Status != StatusReady {
		t.Errorf("status=%q want ready", got.Status)
	}
	if got.RetryCount != 1 {
		t.Errorf("retry_count=%d want 1", got.RetryCount)
	}
}

func TestReview_RejectReview_TaskFails(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	setReviewPolicy(t, h, id, ReviewPolicyHuman)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	_ = h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor())
	task := h.getTask(t, id)
	if err := h.svc.RejectReview(context.Background(), task.ActiveReviewID.String, "no", "doesn't fit", SystemActor()); err != nil {
		t.Fatalf("RejectReview: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusFailed {
		t.Errorf("status=%q want failed", got)
	}
}

func TestReview_EscalateFromAgent_CreatesHumanReview(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	setReviewPolicy(t, h, id, ReviewPolicyAgent)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	_ = h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor())
	task := h.getTask(t, id)
	agentReviewID := task.ActiveReviewID.String
	if err := h.svc.EscalateReview(context.Background(), agentReviewID, "out of scope", SystemActor()); err != nil {
		t.Fatalf("EscalateReview: %v", err)
	}
	// New active review, different ID, type=human, links back.
	got := h.getTask(t, id)
	if got.Status != StatusReviewing {
		t.Errorf("status=%q want reviewing after escalation", got.Status)
	}
	if !got.ActiveReviewID.Valid || got.ActiveReviewID.String == agentReviewID {
		t.Errorf("active_review_id should point at NEW human review")
	}
	human, _ := h.q.GetAgentReview(context.Background(), got.ActiveReviewID.String)
	if human.ReviewerType != ReviewerHuman {
		t.Errorf("new review type=%q want human", human.ReviewerType)
	}
	if !human.EscalatedFromReviewID.Valid || human.EscalatedFromReviewID.String != agentReviewID {
		t.Errorf("escalated_from_review_id=%v want %s", human.EscalatedFromReviewID, agentReviewID)
	}
	// Original agent review now 'escalated'.
	orig, _ := h.q.GetAgentReview(context.Background(), agentReviewID)
	if orig.Status != ReviewEscalated {
		t.Errorf("agent review status=%q want escalated", orig.Status)
	}
}

func TestReview_Escalate_OnHumanReview_Rejected(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	setReviewPolicy(t, h, id, ReviewPolicyHuman)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	_ = h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor())
	task := h.getTask(t, id)
	err := h.svc.EscalateReview(context.Background(), task.ActiveReviewID.String, "nope", SystemActor())
	if err == nil {
		t.Fatal("expected error: only agent reviews can escalate")
	}
}

func TestReview_DoubleDecide_Rejected(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	setReviewPolicy(t, h, id, ReviewPolicyHuman)
	res, _ := h.svc.Claim(context.Background(), ClaimParams{TaskID: id, SessionID: "s"})
	_ = h.svc.Submit(context.Background(), id, res.RunID, "{}", SystemActor())
	task := h.getTask(t, id)
	rev := task.ActiveReviewID.String
	if err := h.svc.ApproveReview(context.Background(), rev, "", SystemActor()); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	err := h.svc.ApproveReview(context.Background(), rev, "", SystemActor())
	if !errors.Is(err, ErrReviewClosed) {
		t.Fatalf("want ErrReviewClosed, got %v", err)
	}
}
