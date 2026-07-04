package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestTimeline_AttemptLifecycleEventsAreWritten(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)
	h.runLeaf(d.ID)

	events := h.timeline(d.ID)
	want := []string{GoalEventLifecycleChanged, GoalEventAttemptStarted, GoalEventAttemptFinished, GoalEventLifecycleChanged}
	if len(events) != len(want) {
		t.Fatalf("timeline len=%d want %d events=%+v", len(events), len(want), events)
	}
	for i, typ := range want {
		if events[i].EventType != typ {
			t.Fatalf("event[%d]=%q want %q events=%+v", i, events[i].EventType, typ, events)
		}
	}
}

func TestTimeline_HumanMessageReattemptsNonDepBlockedGoal(t *testing.T) {
	h := newHarness(t)
	d, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:      h.userID,
		AgentID:     h.agentID,
		Title:       "root",
		Intent:      "test goal",
		Kind:        KindLeaf,
		Required:    true,
		Convergence: ConvergencePolicy{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	h.activate(d.ID)
	h.exec.fn = func(ExecutorRequest) (ExecutorResult, error) {
		return ExecutorResult{Failed: true, FailReason: "model missed", FailureClass: FailureClassModel}, nil
	}
	h.runLeaf(d.ID)
	blocked := h.get(d.ID)
	if blocked.Lifecycle != LifecycleBlocked || blocked.BlockReason != BlockBudgetExhausted {
		t.Fatalf("after fail lifecycle=%q block=%q want blocked budget", blocked.Lifecycle, blocked.BlockReason)
	}

	const text = "用更直接的方案重试"
	if _, err := h.svc.AddHumanMessage(context.Background(), HumanMessageInput{GoalID: d.ID, Text: text, ResponderUserID: h.userID}); err != nil {
		t.Fatalf("AddHumanMessage: %v", err)
	}
	reopened := h.get(d.ID)
	if reopened.Lifecycle != LifecyclePending || reopened.BlockReason != "" {
		t.Fatalf("after human message lifecycle=%q block=%q want ready", reopened.Lifecycle, reopened.BlockReason)
	}
	var pol ConvergencePolicy
	_ = unmarshalJSON(reopened.ConvergencePolicy, &pol)
	if got := pol.Normalized().MaxAttempts; got != 1 {
		t.Fatalf("max_attempts=%d want unchanged 1", got)
	}
	if reopened.BudgetBonus != 1 {
		t.Fatalf("budget_bonus=%d want 1", reopened.BudgetBonus)
	}

	att, err := h.svc.Claim(context.Background(), d.ID, "w-2", nil)
	if err != nil {
		t.Fatalf("claim after human message: %v", err)
	}
	var in AttemptInput
	if err := unmarshalJSON(att.InputContext, &in); err != nil {
		t.Fatalf("decode input_context: %v", err)
	}
	found := false
	for _, ev := range in.TimelineContext {
		if ev.EventType == GoalEventHumanMessage && ev.Text == text {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeline_context=%+v missing human message %q", in.TimelineContext, text)
	}
}

func TestTimeline_HumanMessageBlockedAuthorizesReattempt(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)
	if err := h.svc.Block(context.Background(), d.ID, BlockBudgetExhausted, UserActor(h.userID)); err != nil {
		t.Fatalf("block budget: %v", err)
	}
	before := h.get(d.ID)
	if _, err := h.svc.AddHumanMessage(context.Background(), HumanMessageInput{GoalID: d.ID, Text: "再试一次", ResponderUserID: h.userID}); err != nil {
		t.Fatalf("AddHumanMessage: %v", err)
	}
	after := h.get(d.ID)
	if after.Lifecycle != LifecyclePending || after.BudgetBonus != before.BudgetBonus+1 {
		t.Fatalf("after human lifecycle/bonus=%q/%d want pending/%d", after.Lifecycle, after.BudgetBonus, before.BudgetBonus+1)
	}
	last := h.timeline(d.ID)[len(h.timeline(d.ID))-1]
	if last.EventType != GoalEventLifecycleChanged {
		t.Fatalf("last event=%q want lifecycle change after authorized message", last.EventType)
	}
}

func TestTimeline_StaleAttemptWritesAreRejected(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)
	att1, err := h.svc.Claim(context.Background(), d.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim1: %v", err)
	}
	if err := h.svc.promoteAttempt(context.Background(), att1.ID, h.worker.leaseUntil()); err != nil {
		t.Fatalf("promote1: %v", err)
	}
	if err := h.svc.ReapAttempt(context.Background(), att1.ID); err != nil {
		t.Fatalf("reap1: %v", err)
	}
	if _, err := h.svc.Claim(context.Background(), d.ID, "w-2", nil); err != nil {
		t.Fatalf("claim2: %v", err)
	}
	beforeTimeline := len(h.timeline(d.ID))
	beforeAcceptance, err := h.q.ListAcceptanceEventByGoal(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("list acceptance before: %v", err)
	}

	if err := h.svc.Submit(context.Background(), att1.ID, AttemptEvidence{Summary: "old"}, AttemptOutput{Summary: "old", Hash: "old"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("stale Submit err=%v want ErrInvalidTransition", err)
	}
	if err := h.svc.FailAttempt(context.Background(), att1.ID, "old fail", FailureClassModel); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("stale FailAttempt err=%v want ErrInvalidTransition", err)
	}
	if err := h.worker.appendCheckEvent(context.Background(), d.ID, att1.ID, AcceptanceItem{ID: "check", Kind: ItemDeterministic, Command: "true"}, CheckResult{Pass: true, ExitCode: 0}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("stale appendCheckEvent err=%v want ErrInvalidTransition", err)
	}

	if got := len(h.timeline(d.ID)); got != beforeTimeline {
		t.Fatalf("timeline events=%d want unchanged %d", got, beforeTimeline)
	}
	afterAcceptance, err := h.q.ListAcceptanceEventByGoal(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("list acceptance after: %v", err)
	}
	if len(afterAcceptance) != len(beforeAcceptance) {
		t.Fatalf("acceptance events=%d want unchanged %d", len(afterAcceptance), len(beforeAcceptance))
	}
}

func (h *harness) timeline(goalID string) []sqlc.AgentGoalEvent {
	h.t.Helper()
	rows, err := h.q.ListGoalEventByGoal(context.Background(), sqlc.ListGoalEventByGoalParams{GoalID: goalID, Limit: 100})
	if err != nil {
		h.t.Fatalf("list timeline: %v", err)
	}
	return rows
}
