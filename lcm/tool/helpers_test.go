package tool

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/db/sqlc"
	"github.com/vaayne/anna/lcm"
)

// setupEngine creates a test LCM engine backed by a temp SQLite DB.
// It returns the engine and its Queries handle for seeding data.
func setupEngine(t *testing.T) (lcm.Engine, *sqlc.Queries) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	engine, err := lcm.NewEngine(dbPath, &lcm.StaticSummarizer{Response: "test"})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	// Open a second DB handle for direct query access in tests.
	db, err := lcm.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return engine, sqlc.New(db)
}

// bootstrapSession creates a session in the engine and returns the conversation ID.
func bootstrapSession(t *testing.T, engine lcm.Engine, q *sqlc.Queries, sessionID string) int64 {
	t.Helper()
	ctx := context.Background()

	if err := engine.Bootstrap(ctx, sessionID); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	conv, err := q.GetConversationBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetConversationBySessionID: %v", err)
	}
	return conv.ID
}

// seedMessages inserts messages and returns their IDs.
func seedMessages(t *testing.T, q *sqlc.Queries, convID int64, contents []string) []int64 {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, len(contents))
	for i, c := range contents {
		msg, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ConversationID: convID,
			Seq:            int64(i + 1),
			Role:           lcm.RoleUser,
			Content:        c,
			TokenCount:     int64(lcm.EstimateTokens(c)),
		})
		if err != nil {
			t.Fatalf("CreateMessage %d: %v", i, err)
		}
		ids[i] = msg.ID
	}
	return ids
}

// seedLeafSummary creates a leaf summary linked to message IDs.
func seedLeafSummary(t *testing.T, q *sqlc.Queries, convID int64, id, content string, msgIDs []int64) {
	t.Helper()
	ctx := context.Background()
	err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID:              id,
		ConversationID:  convID,
		Kind:            lcm.KindLeaf,
		Depth:           0,
		Content:         content,
		TokenCount:      int64(lcm.EstimateTokens(content)),
		EarliestAt:      sql.NullString{String: "2025-01-01 10:00:00", Valid: true},
		LatestAt:        sql.NullString{String: "2025-01-01 11:00:00", Valid: true},
		DescendantCount: int64(len(msgIDs)),
	})
	if err != nil {
		t.Fatalf("CreateSummary %s: %v", id, err)
	}
	for i, mid := range msgIDs {
		err := q.LinkSummaryToMessage(ctx, sqlc.LinkSummaryToMessageParams{
			SummaryID: id, MessageID: mid, Ordinal: int64(i),
		})
		if err != nil {
			t.Fatalf("LinkSummaryToMessage %s-%d: %v", id, mid, err)
		}
	}
}

// seedCondensedSummary creates a condensed summary linked to child summary IDs.
func seedCondensedSummary(t *testing.T, q *sqlc.Queries, convID int64, id, content string, depth int, childIDs []string) {
	t.Helper()
	ctx := context.Background()
	err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID:              id,
		ConversationID:  convID,
		Kind:            lcm.KindCondensed,
		Depth:           int64(depth),
		Content:         content,
		TokenCount:      int64(lcm.EstimateTokens(content)),
		EarliestAt:      sql.NullString{String: "2025-01-01 10:00:00", Valid: true},
		LatestAt:        sql.NullString{String: "2025-01-01 12:00:00", Valid: true},
		DescendantCount: int64(len(childIDs)),
	})
	if err != nil {
		t.Fatalf("CreateSummary %s: %v", id, err)
	}
	for i, cid := range childIDs {
		err := q.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{
			SummaryID: cid, ParentSummaryID: id, Ordinal: int64(i),
		})
		if err != nil {
			t.Fatalf("LinkSummaryToParent %s-%s: %v", cid, id, err)
		}
	}
}
