package session

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

// fakeSessionManager returns caller-supplied records so the adapter's fail-closed
// behaviour on corrupt persisted rows can be exercised without a database.
type fakeSessionManager struct {
	rec  memory.SessionInfo
	recs []memory.SessionInfo
}

func (f *fakeSessionManager) SaveInfo(context.Context, memory.SessionInfo) error { return nil }
func (f *fakeSessionManager) RotateInfo(context.Context, string, memory.SessionInfo) error {
	return nil
}

func (f *fakeSessionManager) TouchActiveInfo(context.Context, memory.SessionInfo) (bool, error) {
	return true, nil
}

func (f *fakeSessionManager) LoadInfo(context.Context, string) (memory.SessionInfo, error) {
	return f.rec, nil
}

func (f *fakeSessionManager) ListInfo(context.Context, memory.ListOptions) ([]memory.SessionInfo, error) {
	return f.recs, nil
}

func (f *fakeSessionManager) LoadHistory(context.Context, string) ([]ai.Message, error) {
	return nil, nil
}

// A record whose group_id is set but whose user_id is not the group id violates
// the durable invariant; the persistence boundary must not hand it back as a
// validated Info.
var corruptGroupRecord = memory.SessionInfo{
	ID:      "a:group:11111111-1111-4111-8111-111111111111",
	AgentID: "a",
	UserID:  "some-user", // != GroupID
	GroupID: "11111111-1111-4111-8111-111111111111",
	Kind:    "chat",
}

func TestMemoryAdapter_LoadFailsClosedOnCorruptRecord(t *testing.T) {
	a := newMemoryAdapter(&fakeSessionManager{rec: corruptGroupRecord})
	if _, err := a.load(context.Background(), corruptGroupRecord.ID, corruptGroupRecord.UserID, corruptGroupRecord.AgentID); err == nil {
		t.Fatal("load must fail closed on a record that violates the session invariant")
	}
}

func TestMemoryAdapter_ListFailsClosedOnCorruptRecord(t *testing.T) {
	valid := memory.SessionInfo{ID: "s-priv", AgentID: "a", UserID: "u", Kind: "chat"}
	a := newMemoryAdapter(&fakeSessionManager{recs: []memory.SessionInfo{valid, corruptGroupRecord}})
	if _, err := a.list(context.Background(), "u", "a", memory.ListOptions{}); err == nil {
		t.Fatal("list must fail closed when any record violates the session invariant")
	}
}

// fakeReviewSessionManager also implements ListInfoForReview so the adapter's
// primary (LCM-style) review branch is exercised.
type fakeReviewSessionManager struct {
	fakeSessionManager
	reviewRecs []memory.SessionInfo
}

func (f *fakeReviewSessionManager) ListInfoForReview(context.Context, memory.ListOptions) ([]memory.SessionInfo, error) {
	return f.reviewRecs, nil
}

var (
	ownerlessLegacyRecord = memory.SessionInfo{ID: "s-ownerless", AgentID: "a", UserID: "", Kind: "chat"}
	validReviewRecordA    = memory.SessionInfo{ID: "s-a", AgentID: "a", UserID: "u1", Kind: "chat"}
	validReviewRecordB    = memory.SessionInfo{ID: "s-b", AgentID: "a", UserID: "u2", Kind: "chat"}
)

// TestMemoryAdapter_ListForReviewSkipsOwnerlessKeepsValid proves the review path
// skips a legacy ownerless row (so one such row cannot fail the whole list) while
// returning every valid owned row. Covers both the ListInfoForReview branch and
// the ListInfo fallback used by non-LCM/fake providers.
func TestMemoryAdapter_ListForReviewSkipsOwnerlessKeepsValid(t *testing.T) {
	recs := []memory.SessionInfo{validReviewRecordA, ownerlessLegacyRecord, validReviewRecordB}

	assertKeepsValid := func(t *testing.T, a *memoryAdapter) {
		t.Helper()
		got, err := a.listForReview(context.Background(), "a", memory.ListOptions{})
		if err != nil {
			t.Fatalf("listForReview: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d infos, want 2 (ownerless skipped)", len(got))
		}
		for _, info := range got {
			if info.ID == ownerlessLegacyRecord.ID {
				t.Fatal("ownerless legacy row must be skipped from review")
			}
		}
	}

	t.Run("ListInfoForReview branch", func(t *testing.T) {
		assertKeepsValid(t, newMemoryAdapter(&fakeReviewSessionManager{reviewRecs: recs}))
	})
	t.Run("ListInfo fallback branch", func(t *testing.T) {
		assertKeepsValid(t, newMemoryAdapter(&fakeSessionManager{recs: recs}))
	})
}

// TestMemoryAdapter_ListForReviewFailsClosedOnMalformedOwned proves a non-empty
// malformed record (group mismatch) still fails the review list closed.
func TestMemoryAdapter_ListForReviewFailsClosedOnMalformedOwned(t *testing.T) {
	recs := []memory.SessionInfo{validReviewRecordA, corruptGroupRecord}

	t.Run("ListInfoForReview branch", func(t *testing.T) {
		a := newMemoryAdapter(&fakeReviewSessionManager{reviewRecs: recs})
		if _, err := a.listForReview(context.Background(), "a", memory.ListOptions{}); err == nil {
			t.Fatal("review list must fail closed on a non-empty malformed record")
		}
	})
	t.Run("ListInfo fallback branch", func(t *testing.T) {
		a := newMemoryAdapter(&fakeSessionManager{recs: recs})
		if _, err := a.listForReview(context.Background(), "a", memory.ListOptions{}); err == nil {
			t.Fatal("review list must fail closed on a non-empty malformed record")
		}
	})
}
