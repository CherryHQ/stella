package lcm

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func testDB(t *testing.T) (*sql.DB, *Queries) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, New(db)
}

func TestOpenDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sub", "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Verify file exists with correct permissions
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.IsDir() {
		t.Fatal("expected file, got directory")
	}

	// Verify WAL mode
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}

	// Verify tables exist
	tables := []string{"conversations", "messages", "message_parts", "summaries", "summary_messages", "summary_parents", "context_items"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpenDB_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db1, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("first OpenDB: %v", err)
	}
	_ = db1.Close()

	db2, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("second OpenDB: %v", err)
	}
	_ = db2.Close()
}

func TestConversationCRUD(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, CreateConversationParams{
		SessionID: "sess-1",
		Title:     sql.NullString{String: "Test Session", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", conv.SessionID, "sess-1")
	}

	got, err := q.GetConversationBySessionID(ctx, "sess-1")
	if err != nil {
		t.Fatalf("GetConversationBySessionID: %v", err)
	}
	if got.ID != conv.ID {
		t.Errorf("ID = %d, want %d", got.ID, conv.ID)
	}

	err = q.UpdateConversationTitle(ctx, UpdateConversationTitleParams{
		Title: sql.NullString{String: "Updated", Valid: true},
		ID:    conv.ID,
	})
	if err != nil {
		t.Fatalf("UpdateConversationTitle: %v", err)
	}

	got, err = q.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.Title.String != "Updated" {
		t.Errorf("Title = %q, want %q", got.Title.String, "Updated")
	}
}

func TestMessageCRUD(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg, err := q.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ID,
		Seq:            1,
		Role:           RoleUser,
		Content:        "hello world",
		TokenCount:     3,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	msgs, err := q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetMessagesByConversation: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "hello world" {
		t.Errorf("Content = %q, want %q", msgs[0].Content, "hello world")
	}

	count, err := q.GetMessageCount(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetMessageCount: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	maxSeq, err := q.GetMaxSeq(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetMaxSeq: %v", err)
	}
	if maxSeq != 1 {
		t.Errorf("maxSeq = %d, want 1", maxSeq)
	}

	// Message parts
	err = q.CreateMessagePart(ctx, CreateMessagePartParams{
		ID:          "part-1",
		MessageID:   msg.ID,
		PartType:    PartTypeText,
		Ordinal:     0,
		TextContent: sql.NullString{String: "hello world", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateMessagePart: %v", err)
	}

	parts, err := q.GetMessageParts(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMessageParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts len = %d, want 1", len(parts))
	}
	if parts[0].TextContent.String != "hello world" {
		t.Errorf("TextContent = %q, want %q", parts[0].TextContent.String, "hello world")
	}
}

func TestSummaryCRUD(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg, err := q.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ID, Seq: 1, Role: RoleUser, Content: "test", TokenCount: 1,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	err = q.CreateSummary(ctx, CreateSummaryParams{
		ID: "sum_0001", ConversationID: conv.ID, Kind: KindLeaf,
		Depth: 0, Content: "leaf summary", TokenCount: 5,
	})
	if err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	err = q.LinkSummaryToMessage(ctx, LinkSummaryToMessageParams{
		SummaryID: "sum_0001", MessageID: msg.ID, Ordinal: 0,
	})
	if err != nil {
		t.Fatalf("LinkSummaryToMessage: %v", err)
	}

	linkedMsgs, err := q.GetSummaryMessages(ctx, "sum_0001")
	if err != nil {
		t.Fatalf("GetSummaryMessages: %v", err)
	}
	if len(linkedMsgs) != 1 || linkedMsgs[0].ID != msg.ID {
		t.Errorf("GetSummaryMessages: got %v, want message %d", linkedMsgs, msg.ID)
	}

	// Condensed summary with parent link
	err = q.CreateSummary(ctx, CreateSummaryParams{
		ID: "sum_0002", ConversationID: conv.ID, Kind: KindCondensed,
		Depth: 1, Content: "condensed", TokenCount: 3,
	})
	if err != nil {
		t.Fatalf("CreateSummary condensed: %v", err)
	}

	err = q.LinkSummaryToParent(ctx, LinkSummaryToParentParams{
		SummaryID: "sum_0002", ParentSummaryID: "sum_0001", Ordinal: 0,
	})
	if err != nil {
		t.Fatalf("LinkSummaryToParent: %v", err)
	}

	parents, err := q.GetSummaryParents(ctx, "sum_0002")
	if err != nil {
		t.Fatalf("GetSummaryParents: %v", err)
	}
	if len(parents) != 1 || parents[0].ID != "sum_0001" {
		t.Errorf("GetSummaryParents: got %v", parents)
	}

	children, err := q.GetSummaryChildren(ctx, "sum_0001")
	if err != nil {
		t.Fatalf("GetSummaryChildren: %v", err)
	}
	if len(children) != 1 || children[0].ID != "sum_0002" {
		t.Errorf("GetSummaryChildren: got %v", children)
	}
}

func TestContextItemsCRUD(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg1, err := q.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ID, Seq: 1, Role: RoleUser, Content: "msg1", TokenCount: 2,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	msg2, err := q.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ID, Seq: 2, Role: RoleAssistant, Content: "msg2", TokenCount: 3,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	// Append context items
	err = q.AppendContextItem(ctx, AppendContextItemParams{
		ConversationID: conv.ID, Ordinal: 0, ItemType: ItemTypeMessage,
		MessageID: sql.NullInt64{Int64: msg1.ID, Valid: true},
	})
	if err != nil {
		t.Fatalf("AppendContextItem 0: %v", err)
	}

	err = q.AppendContextItem(ctx, AppendContextItemParams{
		ConversationID: conv.ID, Ordinal: 1, ItemType: ItemTypeMessage,
		MessageID: sql.NullInt64{Int64: msg2.ID, Valid: true},
	})
	if err != nil {
		t.Fatalf("AppendContextItem 1: %v", err)
	}

	items, err := q.GetContextItems(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}

	tokenCount, err := q.GetContextTokenCount(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetContextTokenCount: %v", err)
	}
	if tokenCount != 5 { // 2 + 3
		t.Errorf("tokenCount = %d, want 5", tokenCount)
	}

	// Delete range and verify
	err = q.DeleteContextItemsInRange(ctx, DeleteContextItemsInRangeParams{
		ConversationID: conv.ID, Ordinal: 0, Ordinal_2: 0,
	})
	if err != nil {
		t.Fatalf("DeleteContextItemsInRange: %v", err)
	}

	items, err = q.GetContextItems(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetContextItems after delete: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
}

func TestSearchMessages(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	_, err = q.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ID, Seq: 1, Role: RoleUser, Content: "implement authentication", TokenCount: 5,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	_, err = q.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ID, Seq: 2, Role: RoleAssistant, Content: "done with database", TokenCount: 4,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	results, err := q.SearchMessages(ctx, SearchMessagesParams{
		ConversationID: conv.ID, Content: "%auth%", Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("SearchMessages: got %d results, want 1", len(results))
	}
}
