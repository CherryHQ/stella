package lcm_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestProviderProfileUsesFactsAndSnapshotVersions(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	profiles := memory.ProfileStore(p)
	versioned := memory.VersionedProfileStore(p)
	q := sqlc.New(db)

	if err := profiles.SetProfile(ctx, testUserID, "test", "first profile"); err != nil {
		t.Fatalf("set first profile: %v", err)
	}
	mem, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: testUserID, AgentID: "test"})
	if err != nil {
		t.Fatalf("get memory version: %v", err)
	}
	frozenVersion := mem.Version

	if err := profiles.SetProfile(ctx, testUserID, "test", "second profile"); err != nil {
		t.Fatalf("set second profile: %v", err)
	}

	current, err := profiles.GetProfile(ctx, testUserID, "test")
	if err != nil {
		t.Fatalf("get current profile: %v", err)
	}
	if current != "second profile" {
		t.Fatalf("current profile = %q, want second profile", current)
	}

	frozen, err := versioned.GetProfileAt(ctx, testUserID, "test", frozenVersion)
	if err != nil {
		t.Fatalf("get frozen profile: %v", err)
	}
	if frozen != "first profile" {
		t.Fatalf("frozen profile = %q, want first profile", frozen)
	}

	active, err := q.ListActiveFactsBySubject(ctx, sqlc.ListActiveFactsBySubjectParams{
		UserID:  testUserID,
		AgentID: "test",
		Subject: string(memory.FactSubjectUser),
	})
	if err != nil {
		t.Fatalf("list profile facts: %v", err)
	}
	if len(active) != 1 || active[0].Content != "second profile" {
		t.Fatalf("active profile facts = %#v, want one second profile fact", active)
	}
}

func TestProviderSoulUsesFactsAndSnapshotVersions(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	profiles := memory.ProfileStore(p)
	versioned := memory.VersionedProfileStore(p)
	q := sqlc.New(db)

	if err := profiles.SetAgentSoul(ctx, testUserID, "test", "first soul"); err != nil {
		t.Fatalf("set first soul: %v", err)
	}
	mem, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: testUserID, AgentID: "test"})
	if err != nil {
		t.Fatalf("get memory version: %v", err)
	}
	frozenVersion := mem.Version

	if err := profiles.SetAgentSoul(ctx, testUserID, "test", "second soul"); err != nil {
		t.Fatalf("set second soul: %v", err)
	}

	current, err := profiles.GetAgentSoul(ctx, testUserID, "test")
	if err != nil {
		t.Fatalf("get current soul: %v", err)
	}
	if current != "second soul" {
		t.Fatalf("current soul = %q, want second soul", current)
	}

	frozen, err := versioned.GetAgentSoulAt(ctx, testUserID, "test", frozenVersion)
	if err != nil {
		t.Fatalf("get frozen soul: %v", err)
	}
	if frozen != "first soul" {
		t.Fatalf("frozen soul = %q, want first soul", frozen)
	}
}
