package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// Phase 4 (#525): a plan-backed task's worker prompt carries a goal-context
// packet (goal + accepted plan outline + upstream handoffs), and submit enforces
// a handoff summary. Standalone tasks are unaffected by both.

func (h *testHarness) setOutput(t *testing.T, taskID, status, output string) {
	t.Helper()
	if _, err := h.db.ExecContext(context.Background(),
		`UPDATE agent_task SET status = ?, output = ? WHERE id = ?`, status, output, taskID); err != nil {
		t.Fatalf("set output: %v", err)
	}
}

// The packet for a plan-backed task carries the goal, the accepted plan outline
// (marking the current slice), and the handoff summaries of upstream tasks; the
// rendered prompt contains all of it.
func TestGoalContextPacket_PlanBackedTask(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	tasks := h.planTasks(t, g.ID)
	// design hands off to impl.
	h.setOutput(t, tasks["d"].ID, StatusDone, `{"handoff":{"summary":"chose approach X"}}`)

	packet, err := NewGoalContextPacketBuilder(h.q).Build(context.Background(), tasks["i"].ID)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if packet == nil {
		t.Fatal("packet nil for plan-backed task")
	}
	if packet.CurrentItemID != "i" {
		t.Errorf("current item=%q want i", packet.CurrentItemID)
	}
	if len(packet.Items) != 3 {
		t.Errorf("items=%d want 3", len(packet.Items))
	}
	if len(packet.Upstream) != 1 || packet.Upstream[0].Summary != "chose approach X" {
		t.Fatalf("upstream=%+v want one handoff 'chose approach X'", packet.Upstream)
	}

	prompt := buildTaskPrompt(h.getTask(t, tasks["i"].ID), "", packet)
	for _, want := range []string{"Goal context:", "structured", "chose approach X", "do not create tasks"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// A standalone task has no packet and its prompt is unchanged.
func TestGoalContextPacket_StandaloneTask(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)

	packet, err := NewGoalContextPacketBuilder(h.q).Build(context.Background(), id)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if packet != nil {
		t.Fatalf("standalone task got packet %+v want nil", packet)
	}
	prompt := buildTaskPrompt(h.getTask(t, id), "", packet)
	if strings.Contains(prompt, "Goal context:") {
		t.Error("standalone prompt should not contain goal context")
	}
}

// BLOCKER 1: a staged pending edit must not leak into a running task's packet —
// the packet reads accepted content_json, never pending_content_json.
func TestGoalContextPacket_AcceptedContentStable(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	tasks := h.planTasks(t, g.ID)

	// Stage a pending edit (not materialized) that renames items.
	edited := structuredPlan()
	for i := range edited.Items {
		edited.Items[i].Title = "EDITED-" + edited.Items[i].ID
	}
	raw := mustJSON(t, edited)
	plan, _ := h.q.GetAgentGoalPlanByGoal(context.Background(), g.ID)
	if err := h.q.UpsertAgentGoalPlanPending(context.Background(), sqlc.UpsertAgentGoalPlanPendingParams{
		ID: plan.ID, GoalID: g.ID, Status: PlanStatusDraft, ReviewPolicy: ReviewPolicyNone,
		PendingContentJson: nullable(raw),
	}); err != nil {
		t.Fatalf("stage pending: %v", err)
	}

	packet, err := NewGoalContextPacketBuilder(h.q).Build(context.Background(), tasks["i"].ID)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, it := range packet.Items {
		if strings.HasPrefix(it.Title, "EDITED-") {
			t.Errorf("packet leaked pending edit: item %q title %q", it.ID, it.Title)
		}
	}
}

// Submit on a plan-backed task without a handoff summary is rejected; with one it
// completes. The worker turns the rejection into a retryable protocol_error.
func TestSubmit_PlanBacked_RequiresHandoff(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g, err := f.CreateGoal(ctx, CreateGoalInput{UserID: h.userID, AgentID: h.agentID, Title: "ship"})
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	if err := h.svc.ActivateGoal(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("ActivateGoal: %v", err)
	}
	taskID := h.planTasks(t, g.ID)[directPlanItemID].ID
	res, err := h.svc.Claim(ctx, ClaimParams{
		TaskID: taskID, SessionID: "s", WorkerID: "w", LeaseDuration: 30 * time.Second, Actor: SystemActor(),
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// No handoff -> rejected, task still running (tx rolled back).
	if err := h.svc.Submit(ctx, taskID, res.RunID, `{"result":"done"}`, SystemActor()); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("Submit(no handoff): got %v want ErrInvalidHandoff", err)
	}
	if got := h.getTask(t, taskID).Status; got != StatusRunning {
		t.Fatalf("status=%q want running after rejected submit", got)
	}

	// With handoff -> completes (review_policy none).
	if err := h.svc.Submit(ctx, taskID, res.RunID, `{"handoff":{"summary":"shipped"}}`, SystemActor()); err != nil {
		t.Fatalf("Submit(with handoff): %v", err)
	}
	if got := h.getTask(t, taskID).Status; got != StatusDone {
		t.Errorf("status=%q want done", got)
	}
}

// The handoff gate runs before the review-policy branches: an invalid handoff on
// a review_policy=auto plan task is rejected without opening any review row, and
// the task stays running.
func TestSubmit_PlanBacked_HandoffGate_BeforeReview(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g, err := f.CreateGoal(ctx, CreateGoalInput{UserID: h.userID, AgentID: h.agentID, Title: "ship"})
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	if err := h.svc.ActivateGoal(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("ActivateGoal: %v", err)
	}
	taskID := h.planTasks(t, g.ID)[directPlanItemID].ID
	if _, err := h.db.ExecContext(ctx,
		`UPDATE agent_task SET review_policy = ? WHERE id = ?`, ReviewPolicyAuto, taskID); err != nil {
		t.Fatalf("set review_policy: %v", err)
	}
	res, err := h.svc.Claim(ctx, ClaimParams{TaskID: taskID, SessionID: "s", WorkerID: "w", LeaseDuration: 30 * time.Second})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := h.svc.Submit(ctx, taskID, res.RunID, `{"result":"x"}`, SystemActor()); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("Submit: got %v want ErrInvalidHandoff", err)
	}
	if got := h.getTask(t, taskID).Status; got != StatusRunning {
		t.Errorf("status=%q want running (gate fired before review)", got)
	}
	reviews, _ := h.q.ListAgentReviewsByTask(ctx, sqlc.ListAgentReviewsByTaskParams{TaskID: nullable(taskID), Limit: 10, Offset: 0})
	if len(reviews) != 0 {
		t.Errorf("reviews=%d want 0 (gate must run before review creation)", len(reviews))
	}
}

// A standalone task may submit without a handoff summary.
func TestSubmit_Standalone_NoHandoffRequired(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.createTask(t, StatusReady)
	res, err := h.svc.Claim(ctx, ClaimParams{
		TaskID: id, SessionID: "s", WorkerID: "w", LeaseDuration: 30 * time.Second, Actor: SystemActor(),
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := h.svc.Submit(ctx, id, res.RunID, `{"result":"x"}`, SystemActor()); err != nil {
		t.Fatalf("Submit(standalone): %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusDone {
		t.Errorf("status=%q want done", got)
	}
}
