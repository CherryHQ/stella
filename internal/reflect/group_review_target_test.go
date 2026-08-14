package reflect

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// TestUnreviewedTarget_GroupSessionExcluded proves that a durable GroupID keeps
// a group session out of the private Reflect queue entirely. Filtering before
// target limiting prevents old group sessions from starving private reviews.
func TestUnreviewedTarget_GroupSessionExcluded(t *testing.T) {
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

	registry, err := session.NewRegistry(memorytest.New(), "a")
	if err != nil {
		t.Fatal(err)
	}
	info, err := session.InfoFromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.unreviewedTargetFromRegistry(context.Background(), registry, info); ok {
		t.Fatal("group session must be excluded before entering the private review queue")
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
	registry, err := session.NewRegistry(memorytest.New(), "a")
	if err != nil {
		t.Fatal(err)
	}
	info, err := session.InfoFromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := svc.unreviewedTargetFromRegistry(context.Background(), registry, info)
	if !ok {
		t.Fatal("expected a review target for a fresh private session")
	}
	if !target.privateOneToOne {
		t.Fatal("private session must be classified as private one-to-one")
	}
}
