package reflect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestCandidatePipelineFactSuccessAdvancesOnlyFactWatermark(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)
	wm.setLineMark(target.session.ID, reflectLineSkill, freshAt)

	factCalls := 0
	skillCalls := 0
	result, err := svc.runCandidatePipeline(context.Background(), target, candidatePipelineOptions{
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			factCalls++
			return []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)}, nil
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			skillCalls++
			return []skillCandidate{validSkillCandidate("skill-0001")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if factCalls != 1 || skillCalls != 0 {
		t.Fatalf("unexpected line calls: fact=%d skill=%d", factCalls, skillCalls)
	}
	if !wm.lineMark(target.session.ID, reflectLineFact).Equal(freshAt) {
		t.Fatalf("fact watermark did not advance to %v", freshAt)
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).Equal(freshAt) {
		t.Fatalf("skill watermark should remain at %v", freshAt)
	}
	if !equalRefs(factCandidateRefs(result.FactAccepted), []CandidateRef{"fact-0001"}) {
		t.Fatalf("unexpected fact candidates: %#v", result.FactAccepted)
	}
}

func TestCandidatePipelineSkillSuccessAdvancesOnlySkillWatermark(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)
	wm.setLineMark(target.session.ID, reflectLineFact, freshAt)

	factCalls := 0
	skillCalls := 0
	result, err := svc.runCandidatePipeline(context.Background(), target, candidatePipelineOptions{
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			factCalls++
			return []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)}, nil
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			skillCalls++
			return []skillCandidate{validSkillCandidate("skill-0001")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if factCalls != 0 || skillCalls != 1 {
		t.Fatalf("unexpected line calls: fact=%d skill=%d", factCalls, skillCalls)
	}
	if !wm.lineMark(target.session.ID, reflectLineFact).Equal(freshAt) {
		t.Fatalf("fact watermark should remain at %v", freshAt)
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).Equal(freshAt) {
		t.Fatalf("skill watermark did not advance to %v", freshAt)
	}
	if !equalRefs(skillCandidateRefs(result.SkillAccepted), []CandidateRef{"skill-0001"}) {
		t.Fatalf("unexpected skill candidates: %#v", result.SkillAccepted)
	}
}

func TestCandidatePipelineFactFailureDoesNotBlockSkillWatermark(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)

	result, err := svc.runCandidatePipeline(context.Background(), target, candidatePipelineOptions{
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			return nil, errors.New("fact failed")
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			return []skillCandidate{validSkillCandidate("skill-0001")}, nil
		},
	})
	if err == nil {
		t.Fatal("expected fact line error")
	}

	if !wm.lineMark(target.session.ID, reflectLineFact).IsZero() {
		t.Fatalf("fact watermark should not advance on failure")
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).Equal(freshAt) {
		t.Fatalf("skill watermark did not advance to %v", freshAt)
	}
	if len(result.Errors) != 1 || result.Errors[0].Line != reflectLineFact {
		t.Fatalf("expected fact line error in result, got %#v", result.Errors)
	}
}

func TestCandidatePipelineSkillFailureDoesNotBlockFactWatermark(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)

	result, err := svc.runCandidatePipeline(context.Background(), target, candidatePipelineOptions{
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			return []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)}, nil
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			return nil, errors.New("skill failed")
		},
	})
	if err == nil {
		t.Fatal("expected skill line error")
	}

	if !wm.lineMark(target.session.ID, reflectLineFact).Equal(freshAt) {
		t.Fatalf("fact watermark did not advance to %v", freshAt)
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).IsZero() {
		t.Fatalf("skill watermark should not advance on failure")
	}
	if len(result.Errors) != 1 || result.Errors[0].Line != reflectLineSkill {
		t.Fatalf("expected skill line error in result, got %#v", result.Errors)
	}
}

func TestCandidatePipelineNoFreshContentSkipsLLM(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)
	wm.setLineMark(target.session.ID, reflectLineFact, freshAt)
	wm.setLineMark(target.session.ID, reflectLineSkill, freshAt)

	result, err := svc.runCandidatePipeline(context.Background(), target, candidatePipelineOptions{
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			t.Fatal("fact line should not run without fresh content")
			return nil, nil
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			t.Fatal("skill line should not run without fresh content")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) == 0 {
		t.Fatalf("expected skip reason, got %#v", result)
	}
	if len(result.FactAccepted) != 0 || len(result.SkillAccepted) != 0 {
		t.Fatalf("expected no candidates, got %#v", result)
	}
}

func TestCandidatePipelineOversizedOnlyAdvancesWatermarksWithoutLLM(t *testing.T) {
	fake := memorytest.New()
	wm := newFakeWatermarks()
	svc := &Service{memory: &nonReviewerProvider{fake}, wm: wm, log: testLogger()}

	ctx := context.Background()
	hugeAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "u1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := fake.Append(ctx, sess, ai.UserMessage{Content: strings.Repeat("x", 2000), Timestamp: hugeAt}); err != nil {
		t.Fatal(err)
	}

	result, err := svc.runCandidatePipeline(ctx, reviewTarget{session: sess, privateOneToOne: true}, candidatePipelineOptions{
		ReviewBudget: 32,
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			t.Fatal("fact line should not run for skip-only oversized content")
			return nil, nil
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			t.Fatal("skill line should not run for skip-only oversized content")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !wm.lineMark(sess.ID, reflectLineFact).Equal(hugeAt) {
		t.Fatalf("fact watermark should advance to skipped oversized message %v, got %v", hugeAt, wm.lineMark(sess.ID, reflectLineFact))
	}
	if !wm.lineMark(sess.ID, reflectLineSkill).Equal(hugeAt) {
		t.Fatalf("skill watermark should advance to skipped oversized message %v, got %v", hugeAt, wm.lineMark(sess.ID, reflectLineSkill))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("expected shared oversized skip to be reported once, got %#v", result.Skipped)
	}
}

func TestCandidatePipelineOversizedOnlyAdvancesSeqWatermarksWithoutLLM(t *testing.T) {
	hugeAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	wm := newFakeWatermarks()
	svc := &Service{
		memory: &reviewHistoryProvider{messages: []memory.ReviewMessage{
			{
				ID:       "msg-42",
				FirstSeq: 42,
				LastSeq:  42,
				Message:  ai.UserMessage{Content: strings.Repeat("x", 2000), Timestamp: hugeAt},
			},
		}},
		wm:  wm,
		log: testLogger(),
	}
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "u1"}

	result, err := svc.runCandidatePipeline(context.Background(), reviewTarget{session: sess, privateOneToOne: true}, candidatePipelineOptions{
		ReviewBudget: 32,
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			t.Fatal("fact line should not run for skip-only oversized content")
			return nil, nil
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			t.Fatal("skill line should not run for skip-only oversized content")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wm.lineSeq(sess.ID, reflectLineFact) != 42 {
		t.Fatalf("fact watermark should advance to skipped oversized seq 42, got %d", wm.lineSeq(sess.ID, reflectLineFact))
	}
	if wm.lineSeq(sess.ID, reflectLineSkill) != 42 {
		t.Fatalf("skill watermark should advance to skipped oversized seq 42, got %d", wm.lineSeq(sess.ID, reflectLineSkill))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("expected shared oversized skip to be reported once, got %#v", result.Skipped)
	}
	if result.Skipped[0].FirstSeq != 42 || result.Skipped[0].LastSeq != 42 {
		t.Fatalf("expected oversized skip to keep seq boundary, got %#v", result.Skipped[0])
	}
}

func TestBuildReviewUnitMarksTruncatedWhenBudgetLeavesFreshContent(t *testing.T) {
	t1 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	wm := newFakeWatermarks()
	svc := &Service{
		memory: &reviewHistoryProvider{messages: []memory.ReviewMessage{
			{
				ID:       "msg-1",
				FirstSeq: 1,
				LastSeq:  1,
				Message:  ai.UserMessage{Content: strings.Repeat("a", 80), Timestamp: t1},
			},
			{
				ID:       "msg-2",
				FirstSeq: 2,
				LastSeq:  2,
				Message:  ai.UserMessage{Content: strings.Repeat("b", 80), Timestamp: t2},
			},
		}},
		wm:  wm,
		log: testLogger(),
	}

	unit, err := svc.buildReviewUnit(context.Background(), reviewTarget{
		session:         memory.Session{ID: "s1", AgentID: "a", UserID: "u1"},
		privateOneToOne: true,
	}, reviewWatermark{}, 32)
	if err != nil {
		t.Fatal(err)
	}

	if !unit.Truncated {
		t.Fatalf("expected truncated review unit, got %#v", unit)
	}
	if unit.FreshCount != 1 || unit.LastIncludedSeq != 1 {
		t.Fatalf("expected only first message included, got fresh=%d lastSeq=%d", unit.FreshCount, unit.LastIncludedSeq)
	}
	if !strings.Contains(unit.Text, strings.Repeat("a", 80)) || strings.Contains(unit.Text, strings.Repeat("b", 80)) {
		t.Fatalf("unexpected review unit text: %q", unit.Text)
	}
}

func TestCandidatePipelineReturnsAcceptedCandidatesInMemory(t *testing.T) {
	svc, target, _, _ := newCandidatePipelineTestService(t)

	result, err := svc.runCandidatePipeline(context.Background(), target, candidatePipelineOptions{
		FactLine: func(_ context.Context, unit ReviewUnit) ([]factCandidate, error) {
			return []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)}, nil
		},
		SkillLine: func(_ context.Context, unit ReviewUnit) ([]skillCandidate, error) {
			return []skillCandidate{validSkillCandidate("skill-0001")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equalRefs(factCandidateRefs(result.FactAccepted), []CandidateRef{"fact-0001"}) {
		t.Fatalf("unexpected fact candidates: %#v", result.FactAccepted)
	}
	if !equalRefs(skillCandidateRefs(result.SkillAccepted), []CandidateRef{"skill-0001"}) {
		t.Fatalf("unexpected skill candidates: %#v", result.SkillAccepted)
	}
}

func newCandidatePipelineTestService(t *testing.T) (*Service, reviewTarget, *fakeWatermarks, time.Time) {
	t.Helper()
	fake := memorytest.New()
	wm := newFakeWatermarks()
	svc := &Service{memory: &nonReviewerProvider{fake}, wm: wm, log: testLogger()}

	ctx := context.Background()
	freshAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "u1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := fake.Append(ctx, sess, ai.UserMessage{Content: "fresh learning", Timestamp: freshAt}); err != nil {
		t.Fatal(err)
	}

	return svc, reviewTarget{session: sess, privateOneToOne: true}, wm, freshAt
}

func factCandidateRefs(candidates []factCandidate) []CandidateRef {
	refs := make([]CandidateRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	return refs
}

func skillCandidateRefs(candidates []skillCandidate) []CandidateRef {
	refs := make([]CandidateRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Ref)
	}
	return refs
}
