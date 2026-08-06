package skills

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestHomeSkillUsageStoreLogicalLifecycle(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	store, err := NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatalf("NewHomeSkillUsageStore: %v", err)
	}
	old := testHomeUsageIdentity(t, userID, agentID, "logical-usage", testDigest('a'))

	changed, err := store.InitializeReflectCreate(ctx, old)
	if err != nil || !changed {
		t.Fatalf("InitializeReflectCreate = (%v, %v), want (true, nil)", changed, err)
	}
	initial, err := store.Get(ctx, old)
	if err != nil {
		t.Fatalf("Get initial: %v", err)
	}
	if initial.UseCount != 1 || initial.LogicalID != old.ID || initial.LastUsedAt.Location() != time.UTC || initial.CreatedAt.Location() != time.UTC {
		t.Fatalf("initial usage = %+v, want canonical ID/count/UTC times", initial)
	}

	changed, err = store.InitializeReflectCreate(ctx, old)
	if err != nil || changed {
		t.Fatalf("exact InitializeReflectCreate retry = (%v, %v), want (false, nil)", changed, err)
	}
	retried, err := store.Get(ctx, old)
	if err != nil {
		t.Fatalf("Get retried: %v", err)
	}
	if retried.UseCount != initial.UseCount || !retried.LastUsedAt.Equal(initial.LastUsedAt) || !retried.CreatedAt.Equal(initial.CreatedAt) {
		t.Fatalf("exact create retry changed usage from %+v to %+v", initial, retried)
	}

	if err := store.TouchReflectRuntimeUse(ctx, old); err != nil {
		t.Fatalf("TouchReflectRuntimeUse: %v", err)
	}
	beforePatch, err := store.Get(ctx, old)
	if err != nil {
		t.Fatalf("Get before patch: %v", err)
	}
	time.Sleep(time.Millisecond)
	newDigest := testDigest('b')
	changed, err = store.PatchReflectDigest(ctx, old, newDigest)
	if err != nil || !changed {
		t.Fatalf("PatchReflectDigest = (%v, %v), want (true, nil)", changed, err)
	}
	current := old
	current.LastContentDigest = newDigest
	patched, err := store.Get(ctx, current)
	if err != nil {
		t.Fatalf("Get patched: %v", err)
	}
	if patched.UseCount != beforePatch.UseCount || !patched.LastUsedAt.After(beforePatch.LastUsedAt) {
		t.Fatalf("patch usage = %+v, before = %+v; want preserved count and refreshed time", patched, beforePatch)
	}

	changed, err = store.PatchReflectDigest(ctx, old, testDigest('f'))
	if !errors.Is(err, ErrSkillUsageChanged) || changed {
		t.Fatalf("stale PatchReflectDigest = (%v, %v), want (false, ErrSkillUsageChanged)", changed, err)
	}
	changed, err = store.PatchReflectDigest(ctx, current, newDigest)
	if err != nil || changed {
		t.Fatalf("exact PatchReflectDigest retry = (%v, %v), want (false, nil)", changed, err)
	}
	if err := store.TouchReflectRuntimeUse(ctx, old); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("stale TouchReflectRuntimeUse = %v, want ErrSkillUsageChanged", err)
	}
	if err := store.TouchReflectRuntimeUse(ctx, current); err != nil {
		t.Fatalf("current TouchReflectRuntimeUse: %v", err)
	}
	currentUsage, err := store.Get(ctx, current)
	if err != nil {
		t.Fatalf("Get current: %v", err)
	}
	if currentUsage.UseCount != beforePatch.UseCount+1 {
		t.Fatalf("touched use_count = %d, want %d", currentUsage.UseCount, beforePatch.UseCount+1)
	}

	if err := store.Delete(ctx, current, currentUsage.LastUsedAt.Add(time.Microsecond)); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("stale Delete = %v, want ErrSkillUsageChanged", err)
	}
	if err := store.Delete(ctx, current, currentUsage.LastUsedAt); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, current, currentUsage.LastUsedAt); !errors.Is(err, ErrSkillUsageChanged) {
		t.Fatalf("missing Delete = %v, want ErrSkillUsageChanged", err)
	}
	if _, err := store.Get(ctx, current); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get deleted = %v, want pgx.ErrNoRows", err)
	}
}

func TestHomeSkillUsageStoreRejectsMalformedIdentityBeforeSQL(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	store, err := NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatalf("NewHomeSkillUsageStore: %v", err)
	}
	identity := testHomeUsageIdentity(t, userID, agentID, "identity-check", testDigest('c'))

	wrongOwner := identity
	wrongOwner.UserID = uuid.NewString()
	if _, err := store.InitializeReflectCreate(ctx, wrongOwner); err == nil {
		t.Fatal("InitializeReflectCreate accepted mismatched filesystem identity")
	}
	badDigest := identity
	badDigest.LastContentDigest = "ABC"
	if _, err := store.InitializeReflectCreate(ctx, badDigest); err == nil {
		t.Fatal("InitializeReflectCreate accepted malformed digest")
	}
	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM skill_usage WHERE skill_id = $1`, identity.ID).Scan(&rows); err != nil {
		t.Fatalf("count rejected usage: %v", err)
	}
	if rows != 0 {
		t.Fatalf("malformed identity wrote %d rows, want 0", rows)
	}
}

func TestHomeSkillUsageMutatingTransportErrorsAreOutcomeUnknown(t *testing.T) {
	tests := []struct {
		name string
		call func(*HomeSkillUsageStore, HomeSkillUsageIdentity) error
	}{
		{name: "initialize", call: func(store *HomeSkillUsageStore, identity HomeSkillUsageIdentity) error {
			_, err := store.InitializeReflectCreate(context.Background(), identity)
			return err
		}},
		{name: "patch", call: func(store *HomeSkillUsageStore, identity HomeSkillUsageIdentity) error {
			_, err := store.PatchReflectDigest(context.Background(), identity, testDigest('b'))
			return err
		}},
		{name: "touch", call: func(store *HomeSkillUsageStore, identity HomeSkillUsageIdentity) error {
			return store.TouchReflectRuntimeUse(context.Background(), identity)
		}},
		{name: "delete", call: func(store *HomeSkillUsageStore, identity HomeSkillUsageIdentity) error {
			return store.Delete(context.Background(), identity, time.Now().UTC())
		}},
		{name: "curator delete", call: func(store *HomeSkillUsageStore, identity HomeSkillUsageIdentity) error {
			return store.DeleteForCurator(context.Background(), identity, time.Now().UTC(), time.Now().UTC().Add(time.Hour))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, db, ctx := newTestStore(t)
			userID, agentID := seedFixtures(t, db)
			store, err := NewHomeSkillUsageStore(db)
			if err != nil {
				t.Fatal(err)
			}
			identity := testHomeUsageIdentity(t, userID, agentID, "closed-"+strings.ReplaceAll(tt.name, " ", "-"), testDigest('a'))
			db.Close()
			if err := tt.call(store, identity); !sandbox.IsOutcomeUnknown(err) {
				t.Fatalf("closed-pool mutation = %v, want outcome unknown", err)
			}
			_ = ctx
		})
	}
}

func TestHomeSkillUsageCuratorDeletePinsExactPairLatestActivity(t *testing.T) {
	tests := []struct {
		name       string
		insertMore func(t *testing.T, db *pgxpool.Pool, ctx context.Context, userID, agentID string, latest time.Time)
		wantDelete bool
	}{
		{
			name: "archived newer activity is ignored",
			insertMore: func(t *testing.T, db *pgxpool.Pool, ctx context.Context, userID, agentID string, latest time.Time) {
				t.Helper()
				if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id) VALUES ($1, $2, 'web', 'chat', true, $3, $4, $5)`, uuid.NewString(), "archived-activity", latest.Add(time.Hour), agentID, userID); err != nil {
					t.Fatal(err)
				}
			},
			wantDelete: true,
		},
		{
			name: "new eligible activity changes max",
			insertMore: func(t *testing.T, db *pgxpool.Pool, ctx context.Context, userID, agentID string, latest time.Time) {
				t.Helper()
				if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id) VALUES ($1, $2, 'web', 'chat', false, $3, $4, $5)`, uuid.NewString(), "new-activity", latest.Add(time.Hour), agentID, userID); err != nil {
					t.Fatal(err)
				}
			},
			wantDelete: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, db, ctx := newTestStore(t)
			userID, agentID := seedFixtures(t, db)
			store, err := NewHomeSkillUsageStore(db)
			if err != nil {
				t.Fatal(err)
			}
			identity := testHomeUsageIdentity(t, userID, agentID, "pinned-activity", testDigest('a'))
			if _, err := store.InitializeReflectCreate(ctx, identity); err != nil {
				t.Fatal(err)
			}
			lastUsedAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
			latest := lastUsedAt.Add(time.Hour)
			if _, err := db.Exec(ctx, `UPDATE skill_usage SET last_used_at = $1 WHERE skill_id = $2`, lastUsedAt, identity.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id) VALUES ($1, $2, 'web', 'chat', false, $3, $4, $5)`, uuid.NewString(), "expected-activity", latest, agentID, userID); err != nil {
				t.Fatal(err)
			}
			tt.insertMore(t, db, ctx, userID, agentID, latest)
			err = store.DeleteForCurator(ctx, identity, lastUsedAt, latest)
			if tt.wantDelete {
				if err != nil {
					t.Fatalf("exact curator delete: %v", err)
				}
			} else if !errors.Is(err, ErrSkillUsageChanged) {
				t.Fatalf("changed activity delete = %v, want usage conflict", err)
			}
		})
	}
}

func TestHomeSkillUsageCandidateRejectsMalformedLogicalIdentity(t *testing.T) {
	_, err := homeSkillUsageCandidate(sqlc.ListStaleLogicalReflectSkillUsageForCuratorRow{
		UserID:  uuid.NewString(),
		AgentID: "agent1",
		Name:    "Not-canonical",
	})
	if err == nil {
		t.Fatal("homeSkillUsageCandidate accepted malformed logical identity")
	}
}

func TestHomeSkillUsageStoreMigratedUsageUsesCanonicalFilesystemID(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	store, err := NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatalf("NewHomeSkillUsageStore: %v", err)
	}
	identity := testHomeUsageIdentity(t, userID, agentID, "migrated-logical", testDigest('d'))
	legacyID := "legacy-pg-skill-id"
	now := time.Now().UTC().Truncate(time.Microsecond)
	lastUsedAt := now.Add(-72 * time.Hour)
	if _, err := db.Exec(ctx, `
		INSERT INTO skill_usage (
			skill_id, user_id, agent_id, use_count, last_used_at, created_at,
			scope, name, last_content_digest
		)
		VALUES ($1, $2, $3, 4, $4::timestamptz, $4::timestamptz, 'user_agent', $5, $6)`,
		legacyID, userID, agentID, lastUsedAt, identity.Name, identity.LastContentDigest); err != nil {
		t.Fatalf("seed migrated-style usage: %v", err)
	}

	changed, err := store.InitializeReflectCreate(ctx, identity)
	if err != nil || changed {
		t.Fatalf("InitializeReflectCreate migrated retry = (%v, %v), want (false, nil)", changed, err)
	}
	usage, err := store.Get(ctx, identity)
	if err != nil {
		t.Fatalf("Get migrated usage: %v", err)
	}
	if usage.LogicalID != identity.ID || usage.LogicalID == legacyID {
		t.Fatalf("migrated Get logical ID = %q, want canonical %q (not %q)", usage.LogicalID, identity.ID, legacyID)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id)
		VALUES ($1, $2, 'web', 'chat', false, $3, $4, $5)`, uuid.NewString(), "migrated-logical-usage-candidate", now.Add(-time.Hour), agentID, userID); err != nil {
		t.Fatalf("seed pair activity: %v", err)
	}
	candidates, err := store.ListStaleReflectCandidates(ctx, userID, agentID, now.Add(-48*time.Hour), now.Add(-24*time.Hour), 3)
	if err != nil {
		t.Fatalf("ListStaleReflectCandidates migrated usage: %v", err)
	}
	if len(candidates) != 1 || candidates[0].LogicalID != identity.ID || candidates[0].LogicalID == legacyID {
		t.Fatalf("migrated candidate = %+v, want canonical ID %q (not %q)", candidates, identity.ID, legacyID)
	}
}

func TestHomeSkillUsageStoreCuratorCandidatesUseLogicalUsageAndPairActivity(t *testing.T) {
	_, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	store, err := NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatalf("NewHomeSkillUsageStore: %v", err)
	}
	unused := testHomeUsageIdentity(t, userID, agentID, "unused-logical", testDigest('d'))
	lowUse := testHomeUsageIdentity(t, userID, agentID, "low-use-logical", testDigest('e'))
	for _, identity := range []HomeSkillUsageIdentity{unused, lowUse} {
		if _, err := store.InitializeReflectCreate(ctx, identity); err != nil {
			t.Fatalf("InitializeReflectCreate %s: %v", identity.Name, err)
		}
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.Exec(ctx, `
		UPDATE skill_usage
		SET use_count = CASE WHEN skill_id = $1 THEN 0 ELSE 2 END,
		    last_used_at = CASE WHEN skill_id = $1 THEN $3::timestamptz ELSE $4::timestamptz END
		WHERE skill_id IN ($1, $2)`, unused.ID, lowUse.ID, now.Add(-72*time.Hour), now.Add(-36*time.Hour)); err != nil {
		t.Fatalf("seed logical usage ages: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, archived, last_active, agent_id, user_id)
		VALUES ($1, $2, 'web', 'chat', false, $3, $4, $5)`, uuid.NewString(), "logical-usage-candidate", now.Add(-time.Hour), agentID, userID); err != nil {
		t.Fatalf("seed pair activity: %v", err)
	}

	candidates, err := store.ListStaleReflectCandidates(ctx, userID, agentID, now.Add(-48*time.Hour), now.Add(-24*time.Hour), 3)
	if err != nil {
		t.Fatalf("ListStaleReflectCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2 (%+v)", len(candidates), candidates)
	}
	if candidates[0].LogicalID != unused.ID || candidates[0].Name != unused.Name || candidates[0].LastContentDigest != unused.LastContentDigest || candidates[0].Rule != "unused" {
		t.Fatalf("unused candidate = %+v", candidates[0])
	}
	if candidates[1].LogicalID != lowUse.ID || candidates[1].Rule != "low_use" || !candidates[1].PairLatestActivityAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("low-use candidate = %+v", candidates[1])
	}
}

func testHomeUsageIdentity(t *testing.T, userID, agentID, name, digest string) HomeSkillUsageIdentity {
	t.Helper()
	id, err := encodeFilesystemSkillID("user_agent", userID, agentID, name)
	if err != nil {
		t.Fatalf("encode filesystem skill ID: %v", err)
	}
	return HomeSkillUsageIdentity{ID: id, UserID: userID, AgentID: agentID, Name: name, LastContentDigest: digest}
}

func testDigest(c byte) string { return strings.Repeat(string(c), 64) }
