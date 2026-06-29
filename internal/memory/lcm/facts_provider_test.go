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

func TestProviderProfileHistoryProjectsFactChangelog(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	profiles := memory.ProfileStore(p)
	changelog := memory.ChangelogReader(p)

	if err := profiles.SetProfile(ctx, testUserID, "test", "first profile"); err != nil {
		t.Fatalf("set first profile: %v", err)
	}
	if err := profiles.SetProfile(ctx, testUserID, "test", "second profile"); err != nil {
		t.Fatalf("set second profile: %v", err)
	}

	entries, err := changelog.ReadChangelog(ctx, testUserID, "test", "profile", 10)
	if err != nil {
		t.Fatalf("read profile history: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("profile history entries = %d, want 2", len(entries))
	}
	if entries[0].Scope != "profile" {
		t.Fatalf("latest profile history scope = %q, want profile", entries[0].Scope)
	}
	if entries[0].BeforeText != "first profile" || entries[0].AfterText != "second profile" {
		t.Fatalf("latest profile history before/after = %q/%q, want first/second profile", entries[0].BeforeText, entries[0].AfterText)
	}
	if entries[1].AfterText != "first profile" {
		t.Fatalf("oldest profile history after = %q, want first profile", entries[1].AfterText)
	}
}

func TestProviderIdentityAtFallsBackToLegacyChangelogBeforeFactHistory(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	writer := memory.ChangelogWriter(p)
	versioned := memory.VersionedProfileStore(p)
	v1 := int64(1)
	v2 := int64(2)

	if err := writer.WriteChangelog(ctx, memory.ChangeEntry{
		UserID: testUserID, AgentID: "test", Scope: "profile", Action: "create", Source: memory.SourceManual,
		MemoryVersionAfter: &v1, AfterText: "legacy profile v1",
	}); err != nil {
		t.Fatalf("write legacy profile changelog v1: %v", err)
	}
	if err := writer.WriteChangelog(ctx, memory.ChangeEntry{
		UserID: testUserID, AgentID: "test", Scope: "profile", Action: "update", Source: memory.SourceManual,
		MemoryVersionBefore: &v1, MemoryVersionAfter: &v2, BeforeText: "legacy profile v1", AfterText: "legacy profile v2",
	}); err != nil {
		t.Fatalf("write legacy profile changelog v2: %v", err)
	}
	if err := writer.WriteChangelog(ctx, memory.ChangeEntry{
		UserID: testUserID, AgentID: "test", Scope: "soul", Action: "create", Source: memory.SourceManual,
		MemoryVersionAfter: &v1, AfterText: "legacy soul v1",
	}); err != nil {
		t.Fatalf("write legacy soul changelog v1: %v", err)
	}

	profileAtV1, err := versioned.GetProfileAt(ctx, testUserID, "test", v1)
	if err != nil {
		t.Fatalf("get legacy profile at v1: %v", err)
	}
	if profileAtV1 != "legacy profile v1" {
		t.Fatalf("profile at v1 = %q, want legacy profile v1", profileAtV1)
	}

	soulAtV1, err := versioned.GetAgentSoulAt(ctx, testUserID, "test", v1)
	if err != nil {
		t.Fatalf("get legacy soul at v1: %v", err)
	}
	if soulAtV1 != "legacy soul v1" {
		t.Fatalf("soul at v1 = %q, want legacy soul v1", soulAtV1)
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
