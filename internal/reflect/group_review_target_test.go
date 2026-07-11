package reflect

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

// TestUnreviewedTarget_GroupSessionNotPrivate proves that now GroupID is durable
// (loaded from ctx_conversation.group_id), a group session is classified as
// not-private-one-to-one and is therefore skipped by buildReviewUnit rather than
// reviewed as a personal 1:1 conversation.
func TestUnreviewedTarget_GroupSessionNotPrivate(t *testing.T) {
	svc := &Service{wm: newFakeWatermarks(), log: testLogger()}
	const groupID = "11111111-1111-4111-8111-111111111111"
	rec := memory.SessionInfo{
		ID:         "a:group:" + groupID,
		AgentID:    "a",
		UserID:     groupID, // group sessions are owned by the group
		GroupID:    groupID,
		Channel:    "group:" + groupID,
		Kind:       "chat",
		LastActive: time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
	}

	target, ok := svc.unreviewedTarget(context.Background(), rec)
	if !ok {
		t.Fatal("expected a review target for a fresh group session")
	}
	if target.privateOneToOne {
		t.Fatal("group session must not be classified as private one-to-one")
	}
	if target.sourceGroupID != groupID {
		t.Fatalf("sourceGroupID = %q, want %q", target.sourceGroupID, groupID)
	}

	unit, err := svc.buildReviewUnit(context.Background(), target, reviewWatermark{}, 1000)
	if err != nil {
		t.Fatalf("buildReviewUnit: %v", err)
	}
	if unit.PrivateOneToOne {
		t.Fatal("review unit for a group session must not be private one-to-one")
	}
	if len(unit.Skipped) != 1 || unit.Skipped[0].Reason != reviewSkipNotPrivateOneToOne {
		t.Fatalf("expected a single not-private-one-to-one skip, got %+v", unit.Skipped)
	}
}

// TestUnreviewedTarget_PrivateSessionStaysPrivate is the control: a private
// session (no GroupID) is still classified private one-to-one.
func TestUnreviewedTarget_PrivateSessionStaysPrivate(t *testing.T) {
	svc := &Service{wm: newFakeWatermarks(), log: testLogger()}
	rec := memory.SessionInfo{
		ID:         "s-priv",
		AgentID:    "a",
		UserID:     "user-1",
		Channel:    "web",
		Kind:       "chat",
		LastActive: time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
	}
	target, ok := svc.unreviewedTarget(context.Background(), rec)
	if !ok {
		t.Fatal("expected a review target for a fresh private session")
	}
	if !target.privateOneToOne {
		t.Fatal("private session must be classified as private one-to-one")
	}
	if target.sourceGroupID != "" {
		t.Fatalf("sourceGroupID = %q, want empty", target.sourceGroupID)
	}
}
