package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestCapabilityProbeCreateRejectsRequiredDeterministicWithoutSandbox(t *testing.T) {
	h := newHarness(t)
	h.svc.capabilities = CapabilityProbeFunc(func() bool { return false })

	_, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:   h.userID,
		AgentID:  h.agentID,
		Title:    "root",
		Intent:   "test goal",
		Kind:     KindLeaf,
		Required: true,
		Contract: deterministicContract(0),
	})
	if !errors.Is(err, ErrDeterministicChecksUnsupported) {
		t.Fatalf("CreateRoot err=%v want ErrDeterministicChecksUnsupported", err)
	}
}

func TestCapabilityProbeCreateAllowsJudgmentAndSandboxDeterministic(t *testing.T) {
	h := newHarness(t)
	h.svc.capabilities = CapabilityProbeFunc(func() bool { return false })
	if _, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:   h.userID,
		AgentID:  h.agentID,
		Title:    "judgment",
		Intent:   "test goal",
		Kind:     KindLeaf,
		Required: true,
		Contract: humanJudgmentContract(),
	}); err != nil {
		t.Fatalf("judgment CreateRoot: %v", err)
	}

	h.svc.capabilities = CapabilityProbeFunc(func() bool { return true })
	if _, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:   h.userID,
		AgentID:  h.agentID,
		Title:    "deterministic",
		Intent:   "test goal",
		Kind:     KindLeaf,
		Required: true,
		Contract: deterministicContract(0),
	}); err != nil {
		t.Fatalf("deterministic CreateRoot with capability: %v", err)
	}
}

func TestCapabilityProbeDecompositionFeedsPlannerRepairCode(t *testing.T) {
	h := newHarness(t)
	h.svc.capabilities = CapabilityProbeFunc(func() bool { return false })
	root := h.createRoot(KindComposite, AcceptanceContract{})
	att, err := h.svc.BeginDecomposition(context.Background(), root.ID)
	if err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	if _, err := h.q.PromoteAttempt(context.Background(), sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("PromoteAttempt: %v", err)
	}
	content := DecompositionContent{Children: []ProposedChild{{
		Key: "leaf", Title: "leaf", Kind: KindLeaf, Required: true, AcceptanceContract: deterministicContract(0),
	}}}
	err = h.svc.SubmitDecomposition(context.Background(), att.ID, AttemptEvidence{}, content)
	if !errors.Is(err, ErrDeterministicChecksUnsupported) {
		t.Fatalf("SubmitDecomposition err=%v want ErrDeterministicChecksUnsupported", err)
	}
	errs := decompositionSubmitErrors(root, defaultMaxDepth, content, err)
	if len(errs) != 1 || errs[0].Code != "deterministic_checks_unsupported" {
		t.Fatalf("repair errors=%+v want deterministic_checks_unsupported", errs)
	}
}
