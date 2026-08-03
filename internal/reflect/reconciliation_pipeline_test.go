package reflect

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReconciliationPipelineRunsFactAndSkillConcurrently(t *testing.T) {
	svc, target, _, _ := newCandidatePipelineTestService(t)
	started := make(chan reflectLine, 2)
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := svc.runReconciliationPipeline(context.Background(), target, reconciliationPipelineOptions{
			FactLine: func(_ context.Context, _ ReviewUnit) ([]factCandidateDecision, error) {
				started <- reflectLineFact
				<-release
				return nil, nil
			},
			SkillLine: func(_ context.Context, _ ReviewUnit) ([]skillCandidateDecision, error) {
				started <- reflectLineSkill
				<-release
				return nil, nil
			},
		})
		done <- err
	}()

	seen := map[reflectLine]bool{}
	for len(seen) < 2 {
		select {
		case line := <-started:
			seen[line] = true
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatalf("lines did not overlap; started=%v", seen)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationPipelineFactWriteFailureDoesNotBlockSkillWatermark(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)

	result, err := svc.runReconciliationPipeline(context.Background(), target, reconciliationPipelineOptions{
		FactLine: func(_ context.Context, _ ReviewUnit) ([]factCandidateDecision, error) {
			return []factCandidateDecision{{Candidate: validFactCandidate("fact-0001", factSubjectWorld)}}, nil
		},
		SkillLine: func(_ context.Context, _ ReviewUnit) ([]skillCandidateDecision, error) {
			return []skillCandidateDecision{{Candidate: validSkillCandidate("skill-0001")}}, nil
		},
		FactReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []factCandidateDecision) (reconciliationWriteStats, error) {
			return reconciliationWriteStats{}, errors.New("fact write failed")
		},
		SkillReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []skillCandidateDecision) (reconciliationWriteStats, error) {
			return reconciliationWriteStats{Writes: 1}, nil
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
	if !result.FactStats.Failed || result.SkillStats.Failed || result.SkillStats.Writes != 1 {
		t.Fatalf("line stats did not preserve isolated outcomes: fact=%#v skill=%#v", result.FactStats, result.SkillStats)
	}
}

func TestReconciliationPipelineNoCandidatesAdvancesWatermarkWithoutReconcile(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)
	factReconcilerCalled := false

	result, err := svc.runReconciliationPipeline(context.Background(), target, reconciliationPipelineOptions{
		FactLine: func(_ context.Context, _ ReviewUnit) ([]factCandidateDecision, error) {
			return nil, nil
		},
		SkillLine: func(_ context.Context, _ ReviewUnit) ([]skillCandidateDecision, error) {
			return nil, nil
		},
		FactReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []factCandidateDecision) (reconciliationWriteStats, error) {
			factReconcilerCalled = true
			return reconciliationWriteStats{}, nil
		},
		SkillReconciler: func(_ context.Context, _ reviewTarget, _ ReviewUnit, _ []skillCandidateDecision) (reconciliationWriteStats, error) {
			t.Fatal("skill reconciler should not run without candidates")
			return reconciliationWriteStats{}, nil
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
