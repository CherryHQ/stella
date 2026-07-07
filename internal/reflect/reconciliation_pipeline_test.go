package reflect

import (
	"context"
	"errors"
	"testing"
)

func TestReconciliationPipelineFactWriteFailureDoesNotBlockSkillWatermark(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)

	result, err := svc.runReconciliationPipeline(context.Background(), target, reconciliationPipelineOptions{
		FactLine: func(_ context.Context, _ ReviewUnit) ([]factCandidate, error) {
			return []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)}, nil
		},
		SkillLine: func(_ context.Context, _ ReviewUnit) ([]skillCandidate, error) {
			return []skillCandidate{validSkillCandidate("skill-0001")}, nil
		},
		FactReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []factCandidate) error {
			return errors.New("fact write failed")
		},
		SkillReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []skillCandidate) error {
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected fact reconciliation failure")
	}

	if !wm.lineMark(target.session.ID, reflectLineFact).IsZero() {
		t.Fatalf("fact watermark should not advance on write failure")
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).Equal(freshAt) {
		t.Fatalf("skill watermark should advance after successful write, got %v", wm.lineMark(target.session.ID, reflectLineSkill))
	}
	if len(result.Errors) != 1 || result.Errors[0].Line != reflectLineFact {
		t.Fatalf("expected fact line error, got %#v", result.Errors)
	}
}

func TestReconciliationPipelineNoCandidatesAdvancesWatermarkWithoutReconcile(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)
	factReconcilerCalled := false

	result, err := svc.runReconciliationPipeline(context.Background(), target, reconciliationPipelineOptions{
		FactLine: func(_ context.Context, _ ReviewUnit) ([]factCandidate, error) {
			return nil, nil
		},
		SkillLine: func(_ context.Context, _ ReviewUnit) ([]skillCandidate, error) {
			return nil, nil
		},
		FactReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []factCandidate) error {
			factReconcilerCalled = true
			return nil
		},
		SkillReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []skillCandidate) error {
			t.Fatal("skill reconciler should not run without candidates")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if factReconcilerCalled {
		t.Fatal("fact reconciler should not run without accepted candidates")
	}
	if !wm.lineMark(target.session.ID, reflectLineFact).Equal(freshAt) {
		t.Fatalf("fact watermark should advance, got %v", wm.lineMark(target.session.ID, reflectLineFact))
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).Equal(freshAt) {
		t.Fatalf("skill watermark should advance, got %v", wm.lineMark(target.session.ID, reflectLineSkill))
	}
	if len(result.FactAccepted) != 0 || len(result.SkillAccepted) != 0 {
		t.Fatalf("expected no candidates, got %#v", result)
	}
}
