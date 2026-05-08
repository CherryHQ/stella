package reflect

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/pkg/memory/memorytest"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeWatermarks is a test double for watermarker.
type fakeWatermarks struct {
	marks map[string]time.Time
}

func newFakeWatermarks() *fakeWatermarks {
	return &fakeWatermarks{marks: make(map[string]time.Time)}
}

func (f *fakeWatermarks) get(_ context.Context, sessionID string) (time.Time, error) {
	return f.marks[sessionID], nil
}

func (f *fakeWatermarks) set(_ context.Context, sessionID string, at time.Time) error {
	f.marks[sessionID] = at
	return nil
}

func seedFakeSession(t *testing.T, fake *memorytest.Fake, id, agentID string, userID int64, lastActive time.Time) {
	t.Helper()
	ctx := context.Background()
	sess := memory.Session{ID: id, AgentID: agentID, UserID: userID}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := fake.SaveInfo(ctx, memory.SessionInfo{
		ID:         id,
		AgentID:    agentID,
		UserID:     userID,
		LastActive: lastActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fake.Append(ctx, sess, ai.UserMessage{Content: "hello from " + id, Timestamp: lastActive}); err != nil {
		t.Fatal(err)
	}
}

func TestListUnreviewed_SkipsAnonymous(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: fake, wm: newFakeWatermarks(), batch: 10, log: testLogger()}

	now := time.Now().UTC()
	seedFakeSession(t, fake, "s1", "a", 0, now) // anonymous
	seedFakeSession(t, fake, "s2", "a", 1, now) // has user

	candidates, err := svc.listUnreviewed(context.Background(), fake, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].session.ID != "s2" {
		t.Errorf("expected session s2, got %s", candidates[0].session.ID)
	}
}

func TestListUnreviewed_SkipsAlreadyReviewed(t *testing.T) {
	fake := memorytest.New()
	wm := newFakeWatermarks()
	svc := &Service{memory: fake, wm: wm, batch: 10, log: testLogger()}

	now := time.Now().UTC()
	seedFakeSession(t, fake, "s1", "a", 1, now)
	seedFakeSession(t, fake, "s2", "a", 2, now)

	// Mark s1 as reviewed at or after its LastActive.
	wm.marks["s1"] = now

	candidates, err := svc.listUnreviewed(context.Background(), fake, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].session.ID != "s2" {
		t.Errorf("expected session s2, got %s", candidates[0].session.ID)
	}
}

func TestListUnreviewed_OldestFirst(t *testing.T) {
	fake := memorytest.New()
	wm := newFakeWatermarks()
	svc := &Service{memory: fake, wm: wm, batch: 10, log: testLogger()}

	now := time.Now().UTC()
	seedFakeSession(t, fake, "new", "a", 1, now)
	seedFakeSession(t, fake, "old", "a", 2, now.Add(-2*time.Hour))

	// Give "new" a recent watermark so it still qualifies but is "more recently reviewed".
	wm.marks["new"] = now.Add(-10 * time.Minute)

	candidates, err := svc.listUnreviewed(context.Background(), fake, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	// "old" has zero watermark, so should sort first.
	if candidates[0].session.ID != "old" {
		t.Errorf("expected oldest-first ordering, got %s first", candidates[0].session.ID)
	}
}

func TestListUnreviewed_ZeroWatermarkTiebreaker(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: fake, wm: newFakeWatermarks(), batch: 10, log: testLogger()}

	now := time.Now().UTC()
	// Both sessions have never been reviewed (zero watermark).
	// "older" has an earlier LastActive and should sort first.
	seedFakeSession(t, fake, "newer", "a", 1, now)
	seedFakeSession(t, fake, "older", "a", 2, now.Add(-3*time.Hour))

	candidates, err := svc.listUnreviewed(context.Background(), fake, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	if candidates[0].session.ID != "older" {
		t.Errorf("expected older session first when both have zero watermark, got %s", candidates[0].session.ID)
	}
}

func TestListUnreviewed_BatchLimit(t *testing.T) {
	fake := memorytest.New()
	svc := &Service{memory: fake, wm: newFakeWatermarks(), batch: 2, log: testLogger()}

	now := time.Now().UTC()
	for i := range 5 {
		seedFakeSession(t, fake, fmt.Sprintf("s%d", i), "a", int64(i+1), now.Add(time.Duration(i)*time.Minute))
	}

	candidates, err := svc.listUnreviewed(context.Background(), fake, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates (batch limit), got %d", len(candidates))
	}
}
