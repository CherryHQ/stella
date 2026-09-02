package reflect

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestServiceReconstructionPostgresWatermarksRetryOnlyFailedFactWithoutDuplicatingSkill(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	memoryFixture := memorytest.New()
	session := memory.Session{ID: "reconstruct-watermarks", AgentID: "agent-1", UserID: "user-1"}
	freshAt := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	if err := memoryFixture.Bootstrap(ctx, session); err != nil {
		t.Fatalf("bootstrap memory: %v", err)
	}
	if err := memoryFixture.Append(ctx, session, ai.UserMessage{Content: "durable review candidate", Timestamp: freshAt}); err != nil {
		t.Fatalf("append memory: %v", err)
	}

	newService := func() *Service {
		return New(Config{
			StateStore: testStateStore{store: pluginhost.NewStateStore(db)},
			Memory:     &nonReviewerProvider{inner: memoryFixture},
		})
	}
	target := reviewTarget{session: session, privateOneToOne: true}
	factFailure := errors.New("fact line failed")
	factCalls, skillCalls := 0, 0

	first := newService()
	result, err := first.runReconciliationPipeline(ctx, target, reconciliationPipelineOptions{
		FactLine: func(context.Context, ReviewUnit) ([]factCandidateDecision, error) {
			factCalls++
			return nil, factFailure
		},
		SkillLine: func(context.Context, ReviewUnit) ([]skillCandidateDecision, error) {
			skillCalls++
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("first review succeeded after fact line failure")
	}
	if len(result.Errors) != 1 || result.Errors[0].Line != reflectLineFact || !errors.Is(result.Errors[0].Err, factFailure) {
		t.Fatalf("first review errors = %#v, want only fact failure", result.Errors)
	}
	if factCalls != 1 || skillCalls != 1 {
		t.Fatalf("first review calls: fact=%d skill=%d, want 1/1", factCalls, skillCalls)
	}
	if mark, markErr := first.wm.getLine(ctx, session.ID, reflectLineFact); markErr != nil || mark != (reviewWatermark{}) {
		t.Fatalf("fact watermark after failed line = %#v, %v; want zero", mark, markErr)
	}
	if mark, markErr := first.wm.getLine(ctx, session.ID, reflectLineSkill); markErr != nil || !mark.At.Equal(freshAt) {
		t.Fatalf("skill watermark after committed line = %#v, %v; want %v", mark, markErr, freshAt)
	}

	second := newService()
	result, err = second.runReconciliationPipeline(ctx, target, reconciliationPipelineOptions{
		FactLine: func(context.Context, ReviewUnit) ([]factCandidateDecision, error) {
			factCalls++
			return nil, nil
		},
		SkillLine: func(context.Context, ReviewUnit) ([]skillCandidateDecision, error) {
			skillCalls++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("reconstructed review: %v", err)
	}
	if factCalls != 2 || skillCalls != 1 {
		t.Fatalf("reconstructed review calls: fact=%d skill=%d, want 2/1", factCalls, skillCalls)
	}
	if mark, markErr := second.wm.getLine(ctx, session.ID, reflectLineFact); markErr != nil || !mark.At.Equal(freshAt) {
		t.Fatalf("fact watermark after retry = %#v, %v; want %v", mark, markErr, freshAt)
	}
	if mark, markErr := second.wm.getLine(ctx, session.ID, reflectLineSkill); markErr != nil || !mark.At.Equal(freshAt) {
		t.Fatalf("skill watermark after retry = %#v, %v; want %v", mark, markErr, freshAt)
	}

	third := newService()
	if _, err := third.runReconciliationPipeline(ctx, target, reconciliationPipelineOptions{
		FactLine: func(context.Context, ReviewUnit) ([]factCandidateDecision, error) {
			factCalls++
			return nil, nil
		},
		SkillLine: func(context.Context, ReviewUnit) ([]skillCandidateDecision, error) {
			skillCalls++
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("fully caught-up reconstructed review: %v", err)
	}
	if factCalls != 2 || skillCalls != 1 {
		t.Fatalf("caught-up review ran candidate lines: fact=%d skill=%d, want 2/1", factCalls, skillCalls)
	}
}
