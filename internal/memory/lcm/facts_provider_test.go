package lcm_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
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

func TestProviderVersionZeroSnapshotStaysEmptyAfterFactsAreWritten(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	profiles := memory.ProfileStore(p)
	constraints := memory.ConstraintStore(p)
	facts := memory.FactStore(p)
	versionedProfiles := memory.VersionedProfileStore(p)
	versionedConstraints := memory.VersionedConstraintStore(p)
	versionedFacts := memory.VersionedFactStore(p)
	snapshots := memory.SessionSnapshotStore(p)

	snapshot, err := snapshots.GetOrCreateSessionSnapshot(ctx, "version-zero", testUserID, "test")
	if err != nil {
		t.Fatalf("create session snapshot: %v", err)
	}
	if snapshot.Version != 0 {
		t.Fatalf("snapshot version = %d, want 0", snapshot.Version)
	}

	if err := profiles.SetProfile(ctx, testUserID, "test", "user profile"); err != nil {
		t.Fatalf("set profile: %v", err)
	}
	if err := profiles.SetAgentSoul(ctx, testUserID, "test", "agent soul"); err != nil {
		t.Fatalf("set agent soul: %v", err)
	}
	if _, err := p.ApplyFactBatch(ctx, testUserID, "test", []memorywrite.FactBatchOperation{{
		Action:  memorywrite.FactBatchCreate,
		Subject: memory.FactSubjectWorld,
		Content: "world fact",
	}}); err != nil {
		t.Fatalf("write world fact: %v", err)
	}
	if _, err := constraints.AddConstraint(ctx, testUserID, "test", "constraint"); err != nil {
		t.Fatalf("add constraint: %v", err)
	}

	profileAtSnapshot, err := versionedProfiles.GetProfileAt(ctx, testUserID, "test", snapshot.Version)
	if err != nil {
		t.Fatalf("get profile at snapshot: %v", err)
	}
	if profileAtSnapshot != "" {
		t.Fatalf("profile at version 0 = %q, want empty", profileAtSnapshot)
	}

	soulAtSnapshot, err := versionedProfiles.GetAgentSoulAt(ctx, testUserID, "test", snapshot.Version)
	if err != nil {
		t.Fatalf("get soul at snapshot: %v", err)
	}
	if soulAtSnapshot != "" {
		t.Fatalf("soul at version 0 = %q, want empty", soulAtSnapshot)
	}

	constraintsAtSnapshot, err := versionedConstraints.GetConstraintsAt(ctx, testUserID, "test", snapshot.Version)
	if err != nil {
		t.Fatalf("get constraints at snapshot: %v", err)
	}
	if len(constraintsAtSnapshot) != 0 {
		t.Fatalf("constraints at version 0 = %+v, want empty", constraintsAtSnapshot)
	}

	factsAtSnapshot, err := versionedFacts.ListActiveFactsAt(ctx, testUserID, "test", memory.FactSubjectWorld, snapshot.Version)
	if err != nil {
		t.Fatalf("list facts at snapshot: %v", err)
	}
	if len(factsAtSnapshot) != 0 {
		t.Fatalf("facts at version 0 = %+v, want empty", factsAtSnapshot)
	}

	currentProfile, err := profiles.GetProfile(ctx, testUserID, "test")
	if err != nil {
		t.Fatalf("get current profile: %v", err)
	}
	if currentProfile != "user profile" {
		t.Fatalf("current profile = %q, want user profile", currentProfile)
	}
	currentSoul, err := profiles.GetAgentSoul(ctx, testUserID, "test")
	if err != nil {
		t.Fatalf("get current soul: %v", err)
	}
	if currentSoul != "agent soul" {
		t.Fatalf("current soul = %q, want agent soul", currentSoul)
	}
	currentConstraints, err := constraints.GetConstraints(ctx, testUserID, "test")
	if err != nil {
		t.Fatalf("get current constraints: %v", err)
	}
	if len(currentConstraints) != 1 || currentConstraints[0].Text != "constraint" {
		t.Fatalf("current constraints = %+v, want constraint", currentConstraints)
	}
	currentFacts, err := facts.ListActiveFacts(ctx, testUserID, "test", memory.FactSubjectWorld)
	if err != nil {
		t.Fatalf("list current facts: %v", err)
	}
	if len(currentFacts) != 1 || currentFacts[0].Content != "world fact" {
		t.Fatalf("current facts = %+v, want world fact", currentFacts)
	}
}

func TestProviderConstraintSnapshotBeforeFirstConstraintDoesNotReadCurrent(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	profiles := memory.ProfileStore(p)
	constraints := memory.ConstraintStore(p)
	versionedConstraints := memory.VersionedConstraintStore(p)
	snapshots := memory.SessionSnapshotStore(p)

	if err := profiles.SetProfile(ctx, testUserID, "test", "version one"); err != nil {
		t.Fatalf("write version one profile: %v", err)
	}
	snapshot, err := snapshots.GetOrCreateSessionSnapshot(ctx, "before-first-constraint", testUserID, "test")
	if err != nil {
		t.Fatalf("create session snapshot: %v", err)
	}
	if snapshot.Version != 1 {
		t.Fatalf("snapshot version = %d, want 1", snapshot.Version)
	}
	if _, err := constraints.AddConstraint(ctx, testUserID, "test", "first constraint"); err != nil {
		t.Fatalf("add first constraint: %v", err)
	}

	frozen, err := versionedConstraints.GetConstraintsAt(ctx, testUserID, "test", snapshot.Version)
	if err != nil {
		t.Fatalf("get constraints at version 1: %v", err)
	}
	if len(frozen) != 0 {
		t.Fatalf("constraints at version 1 = %+v, want empty", frozen)
	}

	current, err := constraints.GetConstraints(ctx, testUserID, "test")
	if err != nil {
		t.Fatalf("get current constraints: %v", err)
	}
	if len(current) != 1 || current[0].Text != "first constraint" {
		t.Fatalf("current constraints = %+v, want first constraint", current)
	}
}

func TestProviderVersionedReadsRejectNegativeSnapshotVersion(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	versionedProfiles := memory.VersionedProfileStore(p)
	versionedConstraints := memory.VersionedConstraintStore(p)
	versionedFacts := memory.VersionedFactStore(p)

	if _, err := versionedProfiles.GetProfileAt(ctx, testUserID, "test", -1); err == nil {
		t.Fatal("GetProfileAt with negative version did not return an error")
	}
	if _, err := versionedProfiles.GetAgentSoulAt(ctx, testUserID, "test", -1); err == nil {
		t.Fatal("GetAgentSoulAt with negative version did not return an error")
	}
	if _, err := versionedConstraints.GetConstraintsAt(ctx, testUserID, "test", -1); err == nil {
		t.Fatal("GetConstraintsAt with negative version did not return an error")
	}
	if _, err := versionedFacts.ListActiveFactsAt(ctx, testUserID, "test", memory.FactSubjectWorld, -1); err == nil {
		t.Fatal("ListActiveFactsAt with negative version did not return an error")
	}
}
