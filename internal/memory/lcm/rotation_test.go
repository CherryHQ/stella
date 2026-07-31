package lcm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
)

// rotationScope returns the context the SessionManager methods expect plus a
// main-session record for testUserID.
func rotationScope(t *testing.T) (context.Context, memory.SessionInfo) {
	t.Helper()
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), testUserID), "test")
	return ctx, memory.SessionInfo{
		ID:      "main-" + uuid.NewString(),
		AgentID: "test",
		UserID:  testUserID,
		Channel: "test:user:" + testUserID + ":private",
		Kind:    "main",
	}
}

func newRotationProvider(t *testing.T) *lcm.Provider {
	t.Helper()
	db := newLCMTestDB(t)
	t.Cleanup(db.Close)
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TestRotateInfoReplacesMainAtomically proves the successor is created and the
// predecessor archived in one step, which the idx_one_agent_main partial unique
// index (one active main per agent+user) would reject in any other order.
func TestRotateInfoReplacesMainAtomically(t *testing.T) {
	p := newRotationProvider(t)
	ctx, main := rotationScope(t)
	if err := p.SaveInfo(ctx, main); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	successor := main
	successor.ID = "main-" + uuid.NewString()
	if err := p.RotateInfo(ctx, main.ID, successor); err != nil {
		t.Fatalf("RotateInfo: %v", err)
	}

	predecessor, err := p.LoadInfo(ctx, main.ID)
	if err != nil {
		t.Fatalf("LoadInfo predecessor: %v", err)
	}
	if !predecessor.Archived {
		t.Error("predecessor must be archived")
	}
	created, err := p.LoadInfo(ctx, successor.ID)
	if err != nil {
		t.Fatalf("LoadInfo successor: %v", err)
	}
	if created.Archived || created.Kind != "main" {
		t.Fatalf("successor = %+v, want an active main", created)
	}

	active, err := p.ListInfo(ctx, memory.ListOptions{UserID: testUserID, AgentID: "test", Kind: "main"})
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	if len(active) != 1 || active[0].ID != successor.ID {
		t.Fatalf("active mains = %v, want only the successor", sessionIDs(active))
	}
}

// TestRotateInfoRollsBackFailedSuccessor injects a successor insert failure (a
// session_id that already exists) and proves the whole rotation rolls back: the
// old session is still active and resolvable.
func TestRotateInfoRollsBackFailedSuccessor(t *testing.T) {
	p := newRotationProvider(t)
	ctx, main := rotationScope(t)
	if err := p.SaveInfo(ctx, main); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	occupied := main
	occupied.ID = "chat-" + uuid.NewString()
	occupied.Kind = "chat"
	if err := p.SaveInfo(ctx, occupied); err != nil {
		t.Fatalf("SaveInfo occupied: %v", err)
	}

	successor := main
	successor.ID = occupied.ID // session_id is UNIQUE: the INSERT must fail
	if err := p.RotateInfo(ctx, main.ID, successor); err == nil {
		t.Fatal("RotateInfo must fail when the successor cannot be created")
	}

	predecessor, err := p.LoadInfo(ctx, main.ID)
	if err != nil {
		t.Fatalf("LoadInfo predecessor: %v", err)
	}
	if predecessor.Archived {
		t.Fatal("a failed rotation must leave the predecessor active")
	}
	active, err := p.ListInfo(ctx, memory.ListOptions{UserID: testUserID, AgentID: "test", Kind: "main"})
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	if len(active) != 1 || active[0].ID != main.ID {
		t.Fatalf("active mains = %v, want only the original main", sessionIDs(active))
	}
}

// TestRotateInfoStaleExpectedSession covers the duplicate-/new race: the second
// rotation names a session another rotation already archived, so it must report
// ErrStaleRotation and write nothing.
func TestRotateInfoStaleExpectedSession(t *testing.T) {
	p := newRotationProvider(t)
	ctx, main := rotationScope(t)
	if err := p.SaveInfo(ctx, main); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	first := main
	first.ID = "main-" + uuid.NewString()
	if err := p.RotateInfo(ctx, main.ID, first); err != nil {
		t.Fatalf("first RotateInfo: %v", err)
	}

	second := main
	second.ID = "main-" + uuid.NewString()
	if err := p.RotateInfo(ctx, main.ID, second); !errors.Is(err, memory.ErrStaleRotation) {
		t.Fatalf("stale RotateInfo = %v, want ErrStaleRotation", err)
	}
	if _, err := p.LoadInfo(ctx, second.ID); err == nil {
		t.Error("a stale rotation must not create a successor")
	}
	current, err := p.LoadInfo(ctx, first.ID)
	if err != nil {
		t.Fatalf("LoadInfo current main: %v", err)
	}
	if current.Archived {
		t.Error("a stale rotation must not archive the current main")
	}
}

// TestRotateInfoRejectsBindingMismatch keeps rotation bound to one durable
// binding: an expected session of a different kind is not this binding's
// predecessor, so nothing may be archived or created.
func TestRotateInfoRejectsBindingMismatch(t *testing.T) {
	p := newRotationProvider(t)
	ctx, main := rotationScope(t)
	chat := main
	chat.ID = "chat-" + uuid.NewString()
	chat.Kind = "chat"
	if err := p.SaveInfo(ctx, chat); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	successor := main
	successor.ID = "main-" + uuid.NewString()
	if err := p.RotateInfo(ctx, chat.ID, successor); !errors.Is(err, memory.ErrStaleRotation) {
		t.Fatalf("RotateInfo across kinds = %v, want ErrStaleRotation", err)
	}
	kept, err := p.LoadInfo(ctx, chat.ID)
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if kept.Archived {
		t.Error("a mismatched rotation must not archive the session it named")
	}
}

// TestRotatedSuccessorFreezesCurrentMemoryVersion pins the snapshot contract
// rotation depends on: the successor's first snapshot freezes the memory version
// as of now, and the archived session keeps the version it froze earlier.
func TestRotatedSuccessorFreezesCurrentMemoryVersion(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), testUserID), "test")
	main := memory.SessionInfo{
		ID:      "main-" + uuid.NewString(),
		AgentID: "test",
		UserID:  testUserID,
		Channel: "test:user:" + testUserID + ":private",
		Kind:    "main",
	}
	if err := p.SaveInfo(ctx, main); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	before, err := p.GetOrCreateSessionSnapshot(ctx, main.ID, testUserID, "test")
	if err != nil {
		t.Fatalf("GetOrCreateSessionSnapshot: %v", err)
	}

	// Advance the memory clock between the two sessions.
	if err := p.SetProfile(ctx, testUserID, "test", "likes deterministic tests"); err != nil {
		t.Fatalf("SetProfile: %v", err)
	}
	var current int64
	if err := db.QueryRow(ctx, `SELECT version FROM ctx_agent_memory WHERE user_id = $1 AND agent_id = $2`, testUserID, "test").Scan(&current); err != nil {
		t.Fatalf("read memory version: %v", err)
	}
	if current <= before.Version {
		t.Fatalf("memory version %d did not advance past %d", current, before.Version)
	}

	successor := main
	successor.ID = "main-" + uuid.NewString()
	if err := p.RotateInfo(ctx, main.ID, successor); err != nil {
		t.Fatalf("RotateInfo: %v", err)
	}

	fresh, err := p.GetOrCreateSessionSnapshot(ctx, successor.ID, testUserID, "test")
	if err != nil {
		t.Fatalf("GetOrCreateSessionSnapshot successor: %v", err)
	}
	if fresh.Version != current {
		t.Errorf("successor snapshot version = %d, want the current memory version %d", fresh.Version, current)
	}
	after, err := p.GetOrCreateSessionSnapshot(ctx, main.ID, testUserID, "test")
	if err != nil {
		t.Fatalf("GetOrCreateSessionSnapshot predecessor: %v", err)
	}
	if after.Version != before.Version {
		t.Errorf("archived session snapshot version = %d, want it untouched at %d", after.Version, before.Version)
	}
}
