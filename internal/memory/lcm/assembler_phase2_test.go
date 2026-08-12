package lcm

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestAssemblerSummaryChildrenBatchPreservesXML(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { db.Close() }()

	ctx := context.Background()
	q := sqlc.New(db)
	convID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind) VALUES ($1, $2, 'test', 'chat')`, convID, "sess-summary-children"); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}

	createSummary := func(id, kind string, depth int64, content string) sqlc.CtxSummary {
		t.Helper()
		sum := sqlc.CtxSummary{ID: id, ConversationID: convID, Kind: kind, Depth: depth, Content: content, TokenCount: 1}
		if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ID:                      sum.ID,
			ConversationID:          sum.ConversationID,
			Kind:                    sum.Kind,
			Depth:                   sum.Depth,
			Content:                 sum.Content,
			TokenCount:              sum.TokenCount,
			DescendantCount:         0,
			DescendantTokenCount:    0,
			SourceMessageTokenCount: 0,
		}); err != nil {
			t.Fatalf("create summary %s: %v", id, err)
		}
		return sum
	}

	childrenBySummary := map[string][]sqlc.CtxSummary{
		"sum-child-1": {
			createSummary("sum-constituent-1b", kindLeaf, 0, "constituent 1b"),
			createSummary("sum-constituent-1a", kindLeaf, 0, "constituent 1a"),
		},
		"sum-child-2": {
			createSummary("sum-constituent-2", kindLeaf, 0, "constituent 2"),
		},
		"sum-child-3": {
			createSummary("sum-constituent-3a", kindLeaf, 0, "constituent 3a"),
			createSummary("sum-constituent-3b", kindLeaf, 0, "constituent 3b"),
		},
	}

	children := []sqlc.CtxSummary{
		createSummary("sum-child-1", kindCondensed, 1, "child 1"),
		createSummary("sum-child-2", kindCondensed, 1, "child 2"),
		createSummary("sum-child-3", kindCondensed, 1, "child 3"),
	}
	for i, child := range children {
		if err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
			ConversationID: convID,
			Ordinal:        int64(i + 1),
			ItemType:       itemTypeSummary,
			SummaryID:      pgtype.Text{String: child.ID, Valid: true},
			Role:           "",
		}); err != nil {
			t.Fatalf("append context item %s: %v", child.ID, err)
		}
		for ordinal, constituent := range childrenBySummary[child.ID] {
			if err := q.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{
				SummaryID:       child.ID,
				ParentSummaryID: constituent.ID,
				Ordinal:         int64(ordinal),
			}); err != nil {
				t.Fatalf("link constituent %s -> %s: %v", child.ID, constituent.ID, err)
			}
		}
	}

	msgs, err := newAssembler(q, nil).assemble(ctx, convID, 100_000, 0)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) != len(children) {
		t.Fatalf("assembled %d messages, want %d", len(msgs), len(children))
	}
	for i, child := range children {
		userMsg, ok := msgs[i].(ai.UserMessage)
		if !ok {
			t.Fatalf("msg[%d] = %T, want ai.UserMessage", i, msgs[i])
		}
		got, ok := userMsg.Content.(string)
		if !ok {
			t.Fatalf("msg[%d].Content = %T, want string", i, userMsg.Content)
		}
		want := FormatSummaryXML(child, childrenBySummary[child.ID])
		if got != want {
			t.Fatalf("summary XML for %s changed\ngot:\n%s\nwant:\n%s", child.ID, got, want)
		}
	}
}

func newAssemblerTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.New(t)
}
