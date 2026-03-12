package memory

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/db/sqlc"
)

func testDB(t *testing.T) (*sql.DB, *sqlc.Queries) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, sqlc.New(db)
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

	// Verify user_version is set to current
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
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

func TestMigrateFreshDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Verify new columns exist
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
	}

	// Verify new columns by inserting a row with defaults
	q := sqlc.New(db)
	ctx := context.Background()
	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		SessionID: "test-fresh",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.Channel != "" {
		t.Errorf("Channel = %q, want empty string", conv.Channel)
	}
	if conv.Archived != 0 {
		t.Errorf("Archived = %d, want 0", conv.Archived)
	}
	if conv.LastActive == "" {
		t.Error("LastActive should not be empty")
	}
}

func TestMigrateV1ToV2(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v1.db")

	// Create a v1 database manually (original schema without new columns).
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Create v1 schema (conversations without channel/archived/last_active,
	// messages without event_type).
	v1Schema := `
CREATE TABLE conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE,
    title TEXT,
    bootstrapped_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (conversation_id, seq)
);
`
	if _, err := db.Exec(v1Schema); err != nil {
		t.Fatalf("create v1 schema: %v", err)
	}

	// Insert a v1 conversation and message to verify data survives migration.
	if _, err := db.Exec("INSERT INTO conversations (session_id, title) VALUES ('old-sess', 'Old Title')"); err != nil {
		t.Fatalf("insert v1 conversation: %v", err)
	}
	if _, err := db.Exec("INSERT INTO messages (conversation_id, seq, role, content, token_count) VALUES (1, 1, 'user', 'hello', 2)"); err != nil {
		t.Fatalf("insert v1 message: %v", err)
	}
	_ = db.Close()

	// Now open with OpenDB which should migrate v1 -> v2.
	db2, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB on v1 db: %v", err)
	}
	defer func() { _ = db2.Close() }()

	// Verify version is now 2.
	var version int
	if err := db2.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
	}

	// Verify existing data is preserved and new columns have defaults.
	q := sqlc.New(db2)
	ctx := context.Background()
	conv, err := q.GetConversationBySessionID(ctx, "old-sess")
	if err != nil {
		t.Fatalf("GetConversationBySessionID: %v", err)
	}
	if conv.Title.String != "Old Title" {
		t.Errorf("Title = %q, want %q", conv.Title.String, "Old Title")
	}
	if conv.Channel != "" {
		t.Errorf("Channel = %q, want empty string", conv.Channel)
	}
	if conv.Archived != 0 {
		t.Errorf("Archived = %d, want 0", conv.Archived)
	}

	// Verify messages event_type column was added with default.
	var eventType string
	if err := db2.QueryRow("SELECT event_type FROM messages WHERE id = 1").Scan(&eventType); err != nil {
		t.Fatalf("select event_type: %v", err)
	}
	if eventType != "text" {
		t.Errorf("event_type = %q, want %q", eventType, "text")
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v2.db")

	// First open creates the DB at version 2.
	db1, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("first OpenDB: %v", err)
	}
	_ = db1.Close()

	// Second open should be a no-op (version 2, nothing to do).
	db2, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("second OpenDB: %v", err)
	}
	defer func() { _ = db2.Close() }()

	var version int
	if err := db2.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestConversationCRUD(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
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

	err = q.UpdateConversationTitle(ctx, sqlc.UpdateConversationTitleParams{
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

func TestCreateConversationFull(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
		SessionID:  "sess-full",
		Title:      sql.NullString{String: "Full Conv", Valid: true},
		Channel:    "telegram",
		Archived:   0,
		LastActive: "2025-01-15 10:30:00",
	})
	if err != nil {
		t.Fatalf("CreateConversationFull: %v", err)
	}
	if conv.Channel != "telegram" {
		t.Errorf("Channel = %q, want %q", conv.Channel, "telegram")
	}
	if conv.LastActive != "2025-01-15 10:30:00" {
		t.Errorf("LastActive = %q, want %q", conv.LastActive, "2025-01-15 10:30:00")
	}
	if conv.Archived != 0 {
		t.Errorf("Archived = %d, want 0", conv.Archived)
	}
}

func TestListConversations(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	// Create active and archived conversations.
	_, err := q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
		SessionID:  "active-1",
		Title:      sql.NullString{String: "Active", Valid: true},
		Channel:    "cli",
		Archived:   0,
		LastActive: "2025-01-15 10:00:00",
	})
	if err != nil {
		t.Fatalf("CreateConversationFull active: %v", err)
	}

	_, err = q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
		SessionID:  "archived-1",
		Title:      sql.NullString{String: "Archived", Valid: true},
		Channel:    "telegram",
		Archived:   1,
		LastActive: "2025-01-15 11:00:00",
	})
	if err != nil {
		t.Fatalf("CreateConversationFull archived: %v", err)
	}

	// ListConversations should only return non-archived.
	active, err := q.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("ListConversations len = %d, want 1", len(active))
	}
	if active[0].SessionID != "active-1" {
		t.Errorf("SessionID = %q, want %q", active[0].SessionID, "active-1")
	}

	// ListConversationsAll should return both.
	all, err := q.ListConversationsAll(ctx)
	if err != nil {
		t.Fatalf("ListConversationsAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListConversationsAll len = %d, want 2", len(all))
	}
}

func TestUpdateConversationArchived(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		SessionID: "to-archive",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.Archived != 0 {
		t.Fatalf("Archived = %d, want 0", conv.Archived)
	}

	err = q.UpdateConversationArchived(ctx, sqlc.UpdateConversationArchivedParams{
		Archived:  1,
		SessionID: "to-archive",
	})
	if err != nil {
		t.Fatalf("UpdateConversationArchived: %v", err)
	}

	got, err := q.GetConversationBySessionID(ctx, "to-archive")
	if err != nil {
		t.Fatalf("GetConversationBySessionID: %v", err)
	}
	if got.Archived != 1 {
		t.Errorf("Archived = %d, want 1", got.Archived)
	}
}

func TestUpdateConversationLastActive(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		SessionID: "sess-active",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	originalActive := conv.LastActive

	err = q.UpdateConversationLastActive(ctx, "sess-active")
	if err != nil {
		t.Fatalf("UpdateConversationLastActive: %v", err)
	}

	got, err := q.GetConversationBySessionID(ctx, "sess-active")
	if err != nil {
		t.Fatalf("GetConversationBySessionID: %v", err)
	}
	// last_active should be >= original (they might be equal if same second).
	if got.LastActive < originalActive {
		t.Errorf("LastActive = %q, should be >= %q", got.LastActive, originalActive)
	}
}

func TestUpdateConversationTitleBySessionID(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	_, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		SessionID: "sess-title",
		Title:     sql.NullString{String: "Original", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	err = q.UpdateConversationTitleBySessionID(ctx, sqlc.UpdateConversationTitleBySessionIDParams{
		Title:     sql.NullString{String: "New Title", Valid: true},
		SessionID: "sess-title",
	})
	if err != nil {
		t.Fatalf("UpdateConversationTitleBySessionID: %v", err)
	}

	got, err := q.GetConversationBySessionID(ctx, "sess-title")
	if err != nil {
		t.Fatalf("GetConversationBySessionID: %v", err)
	}
	if got.Title.String != "New Title" {
		t.Errorf("Title = %q, want %q", got.Title.String, "New Title")
	}
}

func TestMessageCRUD(t *testing.T) {
	ctx := context.Background()
	_, q := testDB(t)

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: conv.ID,
		Seq:            1,
		Role:           RoleUser,
		EventType:      EventTypeText,
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
	err = q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{
		ID:          "part-1",
		MessageID:   msg.ID,
		PartType:    "text",
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

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: conv.ID, Seq: 1, Role: RoleUser, EventType: EventTypeText, Content: "test", TokenCount: 1,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	err = q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "sum_0001", ConversationID: conv.ID, Kind: KindLeaf,
		Depth: 0, Content: "leaf summary", TokenCount: 5,
	})
	if err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	err = q.LinkSummaryToMessage(ctx, sqlc.LinkSummaryToMessageParams{
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
	err = q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "sum_0002", ConversationID: conv.ID, Kind: KindCondensed,
		Depth: 1, Content: "condensed", TokenCount: 3,
	})
	if err != nil {
		t.Fatalf("CreateSummary condensed: %v", err)
	}

	err = q.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{
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

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg1, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: conv.ID, Seq: 1, Role: RoleUser, EventType: EventTypeText, Content: "msg1", TokenCount: 2,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	msg2, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: conv.ID, Seq: 2, Role: RoleAssistant, EventType: EventTypeText, Content: "msg2", TokenCount: 3,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	// Append context items
	err = q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: conv.ID, Ordinal: 0, ItemType: ItemTypeMessage,
		MessageID: sql.NullInt64{Int64: msg1.ID, Valid: true},
	})
	if err != nil {
		t.Fatalf("AppendContextItem 0: %v", err)
	}

	err = q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
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
	err = q.DeleteContextItemsInRange(ctx, sqlc.DeleteContextItemsInRangeParams{
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

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	_, err = q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: conv.ID, Seq: 1, Role: RoleUser, EventType: EventTypeText, Content: "implement authentication", TokenCount: 5,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	_, err = q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: conv.ID, Seq: 2, Role: RoleAssistant, EventType: EventTypeText, Content: "done with database", TokenCount: 4,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	results, err := q.SearchMessages(ctx, sqlc.SearchMessagesParams{
		ConversationID: conv.ID, Content: "%auth%", Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("SearchMessages: got %d results, want 1", len(results))
	}
}
