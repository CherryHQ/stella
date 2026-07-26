package reflect

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

// TestReviewKeepsRotatedSessionUntilWatermarkCatchesUp is the reflect-continuity
// contract behind `/new`: rotation archives the previous session immediately, so
// archival must not end review candidacy — only a watermark that has reached the
// session's last message does.
func TestReviewKeepsRotatedSessionUntilWatermarkCatchesUp(t *testing.T) {
	ctx := context.Background()
	fake := memorytest.New()
	wm := newFakeWatermarks()
	svc := &Service{memory: fake, wm: wm, log: testLogger()}
	reg, err := session.NewRegistry(fake, "a")
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	main, err := reg.ResolveMain(ctx, session.MainRequest{UserID: "u1", AgentID: "a"})
	if err != nil {
		t.Fatalf("ResolveMain: %v", err)
	}
	scope, err := reg.MemoryScope(main)
	if err != nil {
		t.Fatalf("MemoryScope: %v", err)
	}
	if err := fake.Append(ctx, scope, ai.UserMessage{Content: "remember this", Timestamp: main.LastActive}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := reg.RotateMain(ctx, session.MainRequest{UserID: "u1", AgentID: "a"}); err != nil {
		t.Fatalf("RotateMain: %v", err)
	}

	targets, err := svc.listUnreviewedFromRegistry(ctx, reg, "a")
	if err != nil {
		t.Fatalf("listUnreviewedFromRegistry: %v", err)
	}
	if !containsTargetSession(targets, main.ID) {
		t.Fatal("the rotated-away session must stay a review target until it is reviewed")
	}

	reviewedAt := main.LastActive.Add(time.Second)
	wm.setLineMark(main.ID, reflectLineFact, reviewedAt)
	wm.setLineMark(main.ID, reflectLineSkill, reviewedAt)

	targets, err = svc.listUnreviewedFromRegistry(ctx, reg, "a")
	if err != nil {
		t.Fatalf("listUnreviewedFromRegistry after review: %v", err)
	}
	if containsTargetSession(targets, main.ID) {
		t.Fatal("a reviewed archived session must drop out of the review queue")
	}
}

func containsTargetSession(targets []reviewTarget, sessionID string) bool {
	for _, target := range targets {
		if target.session.ID == sessionID {
			return true
		}
	}
	return false
}
