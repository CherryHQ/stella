package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestUpdateMetadataContractEditRecoversContractConflictLeaf(t *testing.T) {
	h := newHarness(t)
	d, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:      h.userID,
		AgentID:     h.agentID,
		Title:       "root",
		Intent:      "test goal",
		Kind:        KindLeaf,
		Required:    true,
		Contract:    humanJudgmentContract(),
		Convergence: ConvergencePolicy{MaxAttempts: 3},
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	h.forceBlocked(d.ID, BlockContractConflict, false)
	before := h.get(d.ID)
	beforeBudget := maxAttempts(t, before)

	out, err := h.svc.UpdateMetadata(context.Background(), d.ID, UpdateInput{Contract: ptrContract(updatedJudgmentContract())})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if out.Lifecycle != LifecycleReady || out.BlockReason != "" {
		t.Fatalf("after contract edit lifecycle=%q block=%q want ready", out.Lifecycle, out.BlockReason)
	}
	if got := maxAttempts(t, out); got != beforeBudget {
		t.Fatalf("max_attempts=%d want unchanged %d", got, beforeBudget)
	}
	assertLastLifecycleEvent(t, h, d.ID, LifecycleBlocked, LifecycleReady)
}

func TestUpdateMetadataContractEditRecoversContractConflictComposite(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindComposite, AcceptanceContract{})
	h.forceBlocked(d.ID, BlockContractConflict, true)

	out, err := h.svc.UpdateMetadata(context.Background(), d.ID, UpdateInput{Contract: ptrContract(updatedJudgmentContract())})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if out.Lifecycle != LifecycleActive || out.BlockReason != "" {
		t.Fatalf("after contract edit lifecycle=%q block=%q want active", out.Lifecycle, out.BlockReason)
	}
	assertLastLifecycleEvent(t, h, d.ID, LifecycleBlocked, LifecycleActive)
}

func TestUpdateMetadataWithoutContractLeavesContractConflictBlocked(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, humanJudgmentContract())
	h.forceBlocked(d.ID, BlockContractConflict, false)
	title := "renamed"

	out, err := h.svc.UpdateMetadata(context.Background(), d.ID, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if out.Lifecycle != LifecycleBlocked || out.BlockReason != BlockContractConflict {
		t.Fatalf("after title edit lifecycle=%q block=%q want still blocked/contract_conflict", out.Lifecycle, out.BlockReason)
	}
}

func TestUpdateMetadataRejectedContractDoesNotRecoverContractConflict(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, humanJudgmentContract())
	h.forceBlocked(d.ID, BlockContractConflict, false)
	h.svc.capabilities = CapabilityProbeFunc(func() bool { return false })

	_, err := h.svc.UpdateMetadata(context.Background(), d.ID, UpdateInput{Contract: ptrContract(deterministicContract(0))})
	if !errors.Is(err, ErrDeterministicChecksUnsupported) {
		t.Fatalf("UpdateMetadata err=%v want ErrDeterministicChecksUnsupported", err)
	}
	got := h.get(d.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockContractConflict {
		t.Fatalf("after rejected contract lifecycle=%q block=%q want still blocked/contract_conflict", got.Lifecycle, got.BlockReason)
	}
}

func TestUpdateMetadataContractOnNonBlockedGoalDoesNotChangeLifecycle(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, humanJudgmentContract())

	out, err := h.svc.UpdateMetadata(context.Background(), d.ID, UpdateInput{Contract: ptrContract(updatedJudgmentContract())})
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if out.Lifecycle != LifecycleDraft || out.BlockReason != "" {
		t.Fatalf("after non-blocked contract edit lifecycle=%q block=%q want draft", out.Lifecycle, out.BlockReason)
	}
}

func updatedJudgmentContract() AcceptanceContract {
	return AcceptanceContract{Policy: PolicyDetThenJudgment, Items: []AcceptanceItem{{
		ID: "review", Kind: ItemJudgment, Required: true, Authority: AuthorityHuman, Prompt: "updated?",
	}}}
}

func ptrContract(c AcceptanceContract) *AcceptanceContract { return &c }

func maxAttempts(t *testing.T, d sqlc.AgentGoal) int {
	t.Helper()
	var pol ConvergencePolicy
	if err := unmarshalJSON(d.ConvergencePolicy, &pol); err != nil {
		t.Fatalf("decode convergence_policy: %v", err)
	}
	return pol.Normalized().MaxAttempts
}

func (h *harness) forceBlocked(id, reason string, planned bool) {
	h.t.Helper()
	plannedSQL := "NULL"
	if planned {
		plannedSQL = "now()"
	}
	if _, err := h.db.Exec(context.Background(), `
		UPDATE agent_goal
		SET lifecycle = 'blocked', block_reason = $2, blocked_by = $2, planned_at = `+plannedSQL+`, updated_at = now()
		WHERE id = $1`, id, reason); err != nil {
		h.t.Fatalf("force blocked: %v", err)
	}
}

func assertLastLifecycleEvent(t *testing.T, h *harness, goalID, from, to string) {
	t.Helper()
	events := h.timeline(goalID)
	if len(events) == 0 {
		t.Fatalf("timeline empty")
	}
	last := events[len(events)-1]
	if last.EventType != GoalEventLifecycleChanged {
		t.Fatalf("last event type=%q want lifecycle_changed", last.EventType)
	}
	var payload LifecycleChangedPayload
	if err := unmarshalJSON(last.Payload, &payload); err != nil {
		t.Fatalf("decode lifecycle payload: %v", err)
	}
	if payload.From != from || payload.To != to || payload.BlockReason != "" {
		t.Fatalf("lifecycle payload=%+v want %s→%s with cleared block reason", payload, from, to)
	}
}
