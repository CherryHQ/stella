package memorywrite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestKnowledgeLifecycleListActiveOrdersAndPaginates(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	first := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "first")
	second := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "second")
	third := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "third")
	for _, update := range []struct {
		id string
		at time.Time
	}{
		{first.ID, time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)},
		{second.ID, time.Date(2026, 7, 10, 2, 0, 0, 0, time.UTC)},
		{third.ID, time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)},
	} {
		if _, err := db.Exec(ctx, "UPDATE facts SET updated_at = $1 WHERE id = $2", update.at, update.id); err != nil {
			t.Fatalf("set fact update time: %v", err)
		}
	}

	page, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateActive, Limit: 2,
	})
	if err != nil {
		t.Fatalf("ListKnowledge: %v", err)
	}
	if page.Total != 3 || !page.HasMore || len(page.Items) != 2 {
		t.Fatalf("active page = %#v, want total 3 and two items with more", page)
	}
	if page.Items[0].Fact.ID != third.ID || page.Items[1].Fact.ID != second.ID {
		t.Fatalf("active order = [%s, %s], want [%s, %s]", page.Items[0].Fact.ID, page.Items[1].Fact.ID, third.ID, second.ID)
	}

	page, err = ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateActive, Limit: 2, Cursor: page.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListKnowledge cursor: %v", err)
	}
	if page.Total != 3 || page.HasMore || len(page.Items) != 1 || page.Items[0].Fact.ID != first.ID {
		t.Fatalf("active cursor page = %#v, want final fact", page)
	}
}

func TestKnowledgeLifecycleActiveKeysetSurvivesEarlierInsertAndEqualTimestamp(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sortAt := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	for _, content := range []string{"first", "second", "third"} {
		fact := mustCreateKnowledge(t, ctx, db, q, userID, agentID, content)
		if _, err := db.Exec(ctx, "UPDATE facts SET updated_at = $1 WHERE id = $2", sortAt, fact.ID); err != nil {
			t.Fatalf("set fact update time: %v", err)
		}
	}

	initial, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateActive, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListKnowledge initial: %v", err)
	}
	firstPage, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateActive, Limit: 2,
	})
	if err != nil {
		t.Fatalf("ListKnowledge first page: %v", err)
	}
	if firstPage.NextCursor == nil || !firstPage.HasMore {
		t.Fatalf("first page cursor = %#v, want cursor and more", firstPage)
	}

	// This row sorts before the first page and must not shift the saved boundary.
	inserted := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "inserted before cursor")
	if _, err := db.Exec(ctx, "UPDATE facts SET updated_at = $1 WHERE id = $2", sortAt.Add(time.Hour), inserted.ID); err != nil {
		t.Fatalf("set inserted fact update time: %v", err)
	}

	secondPage, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateActive, Limit: 2, Cursor: firstPage.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListKnowledge second page: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].Fact.ID != initial.Items[2].Fact.ID {
		t.Fatalf("second page = %#v, want pre-existing third row %s without a duplicate", secondPage, initial.Items[2].Fact.ID)
	}

	_, err = ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateActive, Limit: 2,
		Cursor: &KnowledgeCursor{Timestamp: firstPage.NextCursor.Timestamp},
	})
	if err == nil {
		t.Fatal("half-populated knowledge cursor was accepted")
	}

	if _, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateActive, Limit: 0,
	}); err == nil {
		t.Fatal("zero knowledge page limit was accepted")
	}
}

func TestKnowledgeLifecycleRemovedKeysetOrdersByDeprecationAndID(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sortAt := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	for _, content := range []string{"removed first", "removed second", "removed third"} {
		fact := mustCreateKnowledge(t, ctx, db, q, userID, agentID, content)
		if _, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
			FactID: fact.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
		}); err != nil {
			t.Fatalf("DeprecateKnowledge: %v", err)
		}
		if _, err := db.Exec(ctx, `UPDATE ctx_agent_memory_changelog SET created_at = $1
WHERE entity_id = $2 AND scope = 'fact' AND action = 'deprecate'`, sortAt, fact.ID); err != nil {
			t.Fatalf("set deprecation time: %v", err)
		}
	}

	initial, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 10, Now: sortAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("ListKnowledge removed initial: %v", err)
	}
	firstPage, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 2, Now: sortAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("ListKnowledge removed first page: %v", err)
	}
	if firstPage.NextCursor == nil || !firstPage.HasMore {
		t.Fatalf("removed first page cursor = %#v, want cursor and more", firstPage)
	}

	inserted := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "removed inserted before cursor")
	if _, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
		FactID: inserted.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	}); err != nil {
		t.Fatalf("DeprecateKnowledge inserted: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE ctx_agent_memory_changelog SET created_at = $1
WHERE entity_id = $2 AND scope = 'fact' AND action = 'deprecate'`, sortAt.Add(time.Hour), inserted.ID); err != nil {
		t.Fatalf("set inserted deprecation time: %v", err)
	}

	secondPage, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 2, Now: sortAt.Add(2 * time.Hour), Cursor: firstPage.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListKnowledge removed second page: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].Fact.ID != initial.Items[2].Fact.ID {
		t.Fatalf("removed second page = %#v, want ID tie-broken third row %s", secondPage, initial.Items[2].Fact.ID)
	}
}

func TestKnowledgeLifecycleManualCreateAndReflectReplacement(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	created := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "  durable manual content  ")
	if created.Content != "durable manual content" || created.Source != memory.SourceManual || created.Subject != memory.FactSubjectWorld {
		t.Fatalf("created fact = %#v", created)
	}
	if _, err := q.GetKnowledgeUsageForUpdate(ctx, sqlc.GetKnowledgeUsageForUpdateParams{FactID: created.ID, UserID: userID, AgentID: agentID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("manual knowledge usage err = %v, want no rows", err)
	}

	reflectFact, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "reflect content", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("CreateFact reflect: %v", err)
	}
	replaced, err := ReplaceKnowledge(ctx, db, q, KnowledgeReplaceInput{
		FactID: reflectFact.ID, UserID: userID, AgentID: agentID, Content: "  manual replacement  ",
	})
	if err != nil {
		t.Fatalf("ReplaceKnowledge: %v", err)
	}
	if replaced.Content != "manual replacement" || replaced.Source != memory.SourceManual || replaced.Supersedes != reflectFact.ID {
		t.Fatalf("replacement = %#v", replaced)
	}
	old, err := q.GetFact(ctx, sqlc.GetFactParams{ID: reflectFact.ID, UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("GetFact replaced fact: %v", err)
	}
	if old.Status != string(memory.FactStatusDeprecated) {
		t.Fatalf("replaced fact status = %q, want deprecated", old.Status)
	}
}

func TestKnowledgeLifecycleRejectsEmptyContent(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := CreateKnowledge(context.Background(), db, q, KnowledgeCreateInput{
		UserID: userID, AgentID: agentID, Content: " \t\n ",
	})
	if err == nil {
		t.Fatal("CreateKnowledge accepted empty trimmed content")
	}
}

func TestKnowledgeLifecycleConcurrentReplaceHasOneWinner(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	target := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "replace target")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, content := range []string{"replacement one", "replacement two"} {
		go func(content string) {
			<-start
			_, err := ReplaceKnowledge(ctx, db, q, KnowledgeReplaceInput{
				FactID: target.ID, UserID: userID, AgentID: agentID, Content: content,
			})
			results <- err
		}(content)
	}
	close(start)

	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrFactNotRestorable) {
			t.Fatalf("concurrent ReplaceKnowledge err = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent replacement successes = %d, want 1", successes)
	}
}

func TestKnowledgeLifecycleManualDeleteAndRestore(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	created := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "durable content")
	removed, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
		FactID: created.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Status != memory.FactStatusDeprecated {
		t.Fatalf("status = %q", removed.Status)
	}

	removedPage, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 10, Now: removed.UpdatedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("ListKnowledge removed: %v", err)
	}
	if len(removedPage.Items) != 1 || removedPage.Items[0].RemovalSource != KnowledgeRemovalManual || !removedPage.Items[0].IsRestorable {
		t.Fatalf("removed page = %#v", removedPage)
	}
	if removedPage.Items[0].DeprecatedAt == nil || removedPage.Items[0].RestoreDeadline == nil {
		t.Fatalf("removed item missing lifecycle timestamps: %#v", removedPage.Items[0])
	}

	restored, err := RestoreKnowledge(ctx, db, q, KnowledgeRestoreInput{
		FactID: created.ID, UserID: userID, AgentID: agentID, RestoredBy: userID,
		Now: removed.UpdatedAt.Add(89 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Restored || restored.Fact.Source != memory.SourceManual || restored.Fact.Status != memory.FactStatusActive {
		t.Fatalf("restore = %#v", restored)
	}
	noOp, err := RestoreKnowledge(ctx, db, q, KnowledgeRestoreInput{
		FactID: created.ID, UserID: userID, AgentID: agentID, RestoredBy: userID,
	})
	if err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
	if noOp.Restored || noOp.Fact.ID != created.ID {
		t.Fatalf("idempotent restore = %#v", noOp)
	}
	memoryRow, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("GetUserAgentMemory: %v", err)
	}
	if memoryRow.Version != 3 {
		t.Fatalf("memory version = %d, want 3", memoryRow.Version)
	}
	logs, err := q.ListFactChangelogUpToVersion(ctx, sqlc.ListFactChangelogUpToVersionParams{
		UserID: userID, AgentID: agentID, MemoryVersionAfter: pgtype.Int8{Int64: memoryRow.Version, Valid: true},
	})
	if err != nil {
		t.Fatalf("ListFactChangelogUpToVersion: %v", err)
	}
	if len(logs) != 3 || logs[0].Action != "create" || logs[1].Action != "deprecate" || logs[2].Action != "restore" {
		t.Fatalf("lifecycle changelog = %#v", logs)
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(logs[1].Metadata.String), &metadata); err != nil {
		t.Fatalf("parse deprecate metadata: %v", err)
	}
	if metadata["deprecated_by"] != "manual" || metadata["deprecated_by_user_id"] != userID {
		t.Fatalf("deprecate metadata = %#v", metadata)
	}
}

func TestKnowledgeLifecycleFiltersRemovedAndRejectsExpiredOrDuplicateRestore(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	manual := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "manual removed")
	removed, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
		FactID: manual.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	})
	if err != nil {
		t.Fatalf("DeprecateKnowledge: %v", err)
	}

	curator, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "curator removed", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("CreateFact curator: %v", err)
	}
	if _, err := ApplyFactBatch(ctx, db, q, userID, agentID, []FactBatchOperation{{
		Action: FactBatchDeprecateMany, Subject: memory.FactSubjectWorld, TargetFactIDs: []string{curator.ID},
		ChangelogMetadata: json.RawMessage(`{"curator":"usage"}`),
	}}); err != nil {
		t.Fatalf("ApplyFactBatch curator deprecate: %v", err)
	}

	replaced := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "replacement removed")
	if _, err := ReplaceKnowledge(ctx, db, q, KnowledgeReplaceInput{
		FactID: replaced.ID, UserID: userID, AgentID: agentID, Content: "replacement active",
	}); err != nil {
		t.Fatalf("ReplaceKnowledge: %v", err)
	}

	page, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 10, Now: removed.UpdatedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("ListKnowledge removed: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("removed page = %#v, want only manual and curator removals", page)
	}
	expiredPage, err := ListKnowledge(ctx, q, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 10, Now: removed.UpdatedAt.Add(KnowledgeRestoreWindow),
	})
	if err != nil {
		t.Fatalf("ListKnowledge expired removed: %v", err)
	}
	for _, item := range expiredPage.Items {
		if item.Fact.ID == manual.ID {
			t.Fatalf("expired manual fact %s was listed", manual.ID)
		}
	}

	_, err = RestoreKnowledge(ctx, db, q, KnowledgeRestoreInput{
		FactID: manual.ID, UserID: userID, AgentID: agentID, RestoredBy: userID,
		Now: removed.UpdatedAt.Add(KnowledgeRestoreWindow),
	})
	if !errors.Is(err, ErrFactRestoreExpired) {
		t.Fatalf("expired restore err = %v, want ErrFactRestoreExpired", err)
	}

	duplicate := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "duplicate content")
	if _, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
		FactID: duplicate.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	}); err != nil {
		t.Fatalf("deprecate duplicate: %v", err)
	}
	mustCreateKnowledge(t, ctx, db, q, userID, agentID, "duplicate content")
	_, err = RestoreKnowledge(ctx, db, q, KnowledgeRestoreInput{
		FactID: duplicate.ID, UserID: userID, AgentID: agentID, RestoredBy: userID,
	})
	if !errors.Is(err, ErrFactDuplicateContent) {
		t.Fatalf("duplicate restore err = %v, want ErrFactDuplicateContent", err)
	}

	trimmed := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "trimmed duplicate")
	if _, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
		FactID: trimmed.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	}); err != nil {
		t.Fatalf("deprecate trimmed duplicate: %v", err)
	}
	if _, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "  trimmed duplicate  ", Source: memory.SourceReflect,
	}); err != nil {
		t.Fatalf("CreateFact spaced duplicate: %v", err)
	}
	_, err = RestoreKnowledge(ctx, db, q, KnowledgeRestoreInput{
		FactID: trimmed.ID, UserID: userID, AgentID: agentID, RestoredBy: userID,
	})
	if !errors.Is(err, ErrFactDuplicateContent) {
		t.Fatalf("trimmed duplicate restore err = %v, want ErrFactDuplicateContent", err)
	}
}

func TestKnowledgeLifecycleRemovedWindowUsesExactHoursAcrossDST(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	fact := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "DST boundary")
	if _, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
		FactID: fact.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	}); err != nil {
		t.Fatalf("DeprecateKnowledge: %v", err)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin timezone transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// SET LOCAL keeps the DST-sensitive timezone scoped to this transaction.
	if _, err := tx.Exec(ctx, "SET LOCAL TIME ZONE 'America/New_York'"); err != nil {
		t.Fatalf("set local timezone: %v", err)
	}
	deprecatedAt := time.Date(2026, time.January, 1, 13, 0, 0, 0, time.UTC)
	result, err := tx.Exec(ctx, `
		UPDATE ctx_agent_memory_changelog
		SET created_at = $1
		WHERE user_id = $2
		  AND agent_id = $3
		  AND scope = 'fact'
		  AND action = 'deprecate'
		  AND entity_id = $4
	`, deprecatedAt, userID, agentID, fact.ID)
	if err != nil {
		t.Fatalf("set deprecation timestamp: %v", err)
	}
	if result.RowsAffected() != 1 {
		t.Fatalf("updated deprecation rows = %d, want 1", result.RowsAffected())
	}

	qtx := q.WithTx(tx)
	inside, err := ListKnowledge(ctx, qtx, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 10,
		Now: deprecatedAt.Add(KnowledgeRestoreWindow - time.Second),
	})
	if err != nil {
		t.Fatalf("ListKnowledge inside window: %v", err)
	}
	if inside.Total != 1 || len(inside.Items) != 1 || inside.Items[0].Fact.ID != fact.ID {
		t.Fatalf("inside exact-hour window page = %#v, want removed fact", inside)
	}

	atDeadline, err := ListKnowledge(ctx, qtx, KnowledgeListQuery{
		UserID: userID, AgentID: agentID, State: KnowledgeStateRemoved, Limit: 10,
		Now: deprecatedAt.Add(KnowledgeRestoreWindow),
	})
	if err != nil {
		t.Fatalf("ListKnowledge at deadline: %v", err)
	}
	if atDeadline.Total != 0 || len(atDeadline.Items) != 0 {
		t.Fatalf("deadline page = %#v, want expired fact excluded", atDeadline)
	}
}

func TestKnowledgeLifecycleRejectsCrossOwnerAndRestoresReflectUsage(t *testing.T) {
	db, q, userID, agentID, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	fact, err := CreateFact(ctx, db, q, memory.FactWrite{
		UserID: userID, AgentID: agentID, Subject: memory.FactSubjectWorld, Content: "reflect restoration", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("CreateFact: %v", err)
	}
	if _, err := DeprecateKnowledge(ctx, db, q, KnowledgeDeprecateInput{
		FactID: fact.ID, UserID: userID, AgentID: agentID, DeprecatedBy: userID,
	}); err != nil {
		t.Fatalf("DeprecateKnowledge: %v", err)
	}
	if _, err := q.GetKnowledgeUsageForUpdate(ctx, sqlc.GetKnowledgeUsageForUpdateParams{FactID: fact.ID, UserID: userID, AgentID: agentID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("usage after deprecate err = %v, want no rows", err)
	}

	_, err = RestoreKnowledge(ctx, db, q, KnowledgeRestoreInput{
		FactID: fact.ID, UserID: "00000000-0000-0000-0000-000000000001", AgentID: agentID, RestoredBy: userID,
	})
	if !errors.Is(err, ErrFactNotRestorable) {
		t.Fatalf("cross-owner restore err = %v, want ErrFactNotRestorable", err)
	}
	owned := mustCreateKnowledge(t, ctx, db, q, userID, agentID, "cross-owner replacement")
	_, err = ReplaceKnowledge(ctx, db, q, KnowledgeReplaceInput{
		FactID: owned.ID, UserID: "00000000-0000-0000-0000-000000000001", AgentID: agentID, Content: "must not write",
	})
	if !errors.Is(err, ErrFactNotRestorable) {
		t.Fatalf("cross-owner replace err = %v, want ErrFactNotRestorable", err)
	}
	if _, err := RestoreKnowledge(ctx, db, q, KnowledgeRestoreInput{
		FactID: fact.ID, UserID: userID, AgentID: agentID, RestoredBy: userID,
	}); err != nil {
		t.Fatalf("RestoreKnowledge reflect: %v", err)
	}
	if _, err := q.GetKnowledgeUsageForUpdate(ctx, sqlc.GetKnowledgeUsageForUpdateParams{FactID: fact.ID, UserID: userID, AgentID: agentID}); err != nil {
		t.Fatalf("reflect usage after restore: %v", err)
	}
}

func mustCreateKnowledge(t *testing.T, ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, userID string, agentID string, content string) memory.Fact {
	t.Helper()
	fact, err := CreateKnowledge(ctx, db, q, KnowledgeCreateInput{UserID: userID, AgentID: agentID, Content: content})
	if err != nil {
		t.Fatalf("CreateKnowledge: %v", err)
	}
	return fact
}
