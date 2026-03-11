package lcm

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// setupRetrievalTest creates a test DB, a conversation, and returns
// the RetrievalEngine, Queries, and conversation ID.
func setupRetrievalTest(t *testing.T) (*RetrievalEngine, *Queries, int64) {
	t.Helper()
	_, q := testDB(t)

	ctx := context.Background()
	conv, err := q.CreateConversation(ctx, CreateConversationParams{SessionID: "sess-retrieval"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	return &RetrievalEngine{q: q}, q, conv.ID
}

// seedMessages inserts n messages and returns their IDs.
func seedMessages(t *testing.T, q *Queries, convID int64, contents []string) []int64 {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, len(contents))
	for i, c := range contents {
		msg, err := q.CreateMessage(ctx, CreateMessageParams{
			ConversationID: convID,
			Seq:            int64(i + 1),
			Role:           RoleUser,
			Content:        c,
			TokenCount:     int64(EstimateTokens(c)),
		})
		if err != nil {
			t.Fatalf("CreateMessage %d: %v", i, err)
		}
		ids[i] = msg.ID
	}
	return ids
}

// seedLeafSummary creates a leaf summary linked to the given message IDs.
func seedLeafSummary(t *testing.T, q *Queries, convID int64, id string, content string, msgIDs []int64) {
	t.Helper()
	ctx := context.Background()
	err := q.CreateSummary(ctx, CreateSummaryParams{
		ID:              id,
		ConversationID:  convID,
		Kind:            KindLeaf,
		Depth:           0,
		Content:         content,
		TokenCount:      int64(EstimateTokens(content)),
		EarliestAt:      sql.NullString{String: "2025-01-01 10:00:00", Valid: true},
		LatestAt:        sql.NullString{String: "2025-01-01 11:00:00", Valid: true},
		DescendantCount: int64(len(msgIDs)),
	})
	if err != nil {
		t.Fatalf("CreateSummary %s: %v", id, err)
	}
	for i, mid := range msgIDs {
		err := q.LinkSummaryToMessage(ctx, LinkSummaryToMessageParams{
			SummaryID: id, MessageID: mid, Ordinal: int64(i),
		})
		if err != nil {
			t.Fatalf("LinkSummaryToMessage %s-%d: %v", id, mid, err)
		}
	}
}

// seedCondensedSummary creates a condensed summary linked to child summary IDs.
func seedCondensedSummary(t *testing.T, q *Queries, convID int64, id string, content string, depth int, childIDs []string) {
	t.Helper()
	ctx := context.Background()
	err := q.CreateSummary(ctx, CreateSummaryParams{
		ID:              id,
		ConversationID:  convID,
		Kind:            KindCondensed,
		Depth:           int64(depth),
		Content:         content,
		TokenCount:      int64(EstimateTokens(content)),
		EarliestAt:      sql.NullString{String: "2025-01-01 10:00:00", Valid: true},
		LatestAt:        sql.NullString{String: "2025-01-01 12:00:00", Valid: true},
		DescendantCount: int64(len(childIDs)),
	})
	if err != nil {
		t.Fatalf("CreateSummary %s: %v", id, err)
	}
	for i, cid := range childIDs {
		err := q.LinkSummaryToParent(ctx, LinkSummaryToParentParams{
			SummaryID: cid, ParentSummaryID: id, Ordinal: int64(i),
		})
		if err != nil {
			t.Fatalf("LinkSummaryToParent %s-%s: %v", cid, id, err)
		}
	}
}

func TestGrep_Messages(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	seedMessages(t, q, convID, []string{
		"implement authentication module",
		"fix database connection pooling",
		"add authentication tests",
	})

	results, err := r.Grep(ctx, convID, "authentication", "messages", 10)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, res := range results {
		if res.SourceType != "message" {
			t.Errorf("SourceType = %q, want %q", res.SourceType, "message")
		}
		if !strings.Contains(res.Content, "authentication") {
			t.Errorf("Content %q does not contain 'authentication'", res.Content)
		}
	}
}

func TestGrep_Summaries(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{"msg1", "msg2"})
	seedLeafSummary(t, q, convID, "sum_001", "summary about authentication flow", msgIDs)
	seedLeafSummary(t, q, convID, "sum_002", "summary about database schema", msgIDs)

	results, err := r.Grep(ctx, convID, "authentication", "summaries", 10)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].SourceType != "summary" {
		t.Errorf("SourceType = %q, want %q", results[0].SourceType, "summary")
	}
	if results[0].SourceID != "sum_001" {
		t.Errorf("SourceID = %q, want %q", results[0].SourceID, "sum_001")
	}
}

func TestGrep_Both(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{
		"implement auth module",
		"unrelated content",
	})
	seedLeafSummary(t, q, convID, "sum_001", "summary about auth system", msgIDs)

	results, err := r.Grep(ctx, convID, "auth", "", 10) // default scope = "both"
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	hasMsg, hasSum := false, false
	for _, res := range results {
		if res.SourceType == "message" {
			hasMsg = true
		}
		if res.SourceType == "summary" {
			hasSum = true
		}
	}
	if !hasMsg || !hasSum {
		t.Errorf("expected both message and summary results, got msg=%v sum=%v", hasMsg, hasSum)
	}
}

func TestGrep_DefaultLimit(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	// Seed more messages than default limit.
	contents := make([]string, 25)
	for i := range contents {
		contents[i] = "searchable content item"
	}
	seedMessages(t, q, convID, contents)

	results, err := r.Grep(ctx, convID, "searchable", "messages", 0) // 0 triggers default
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(results) != defaultGrepLimit {
		t.Errorf("got %d results, want %d (default limit)", len(results), defaultGrepLimit)
	}
}

func TestGrep_NoResults(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	seedMessages(t, q, convID, []string{"hello world"})

	results, err := r.Grep(ctx, convID, "nonexistent", "both", 10)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestGrep_ContentTruncation(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	// Create a message with content longer than maxContentSnippet runes.
	longContent := strings.Repeat("x", 600) + " findme"
	seedMessages(t, q, convID, []string{longContent})

	results, err := r.Grep(ctx, convID, "findme", "messages", 10)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	// The LIKE search in SQLite searches the full content, so it should match.
	// But the returned Content should be truncated.
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if len([]rune(results[0].Content)) > maxContentSnippet+3 { // +3 for "..."
		t.Errorf("content not truncated: %d runes", len([]rune(results[0].Content)))
	}
}

func TestDescribe(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{"m1", "m2", "m3"})

	// Create leaf summaries.
	seedLeafSummary(t, q, convID, "leaf_001", "first leaf summary", msgIDs[:2])
	seedLeafSummary(t, q, convID, "leaf_002", "second leaf summary", msgIDs[2:])

	// Create condensed parent.
	seedCondensedSummary(t, q, convID, "cond_001", "condensed summary", 1, []string{"leaf_001", "leaf_002"})

	// Describe the condensed summary.
	desc, err := r.Describe(ctx, "cond_001")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.SummaryID != "cond_001" {
		t.Errorf("SummaryID = %q, want %q", desc.SummaryID, "cond_001")
	}
	if desc.Kind != KindCondensed {
		t.Errorf("Kind = %q, want %q", desc.Kind, KindCondensed)
	}
	if desc.Depth != 1 {
		t.Errorf("Depth = %d, want 1", desc.Depth)
	}
	if len(desc.ChildIDs) != 2 {
		t.Errorf("ChildIDs len = %d, want 2", len(desc.ChildIDs))
	}
	if len(desc.ParentIDs) != 0 {
		t.Errorf("ParentIDs len = %d, want 0", len(desc.ParentIDs))
	}

	// Describe a leaf with parent.
	desc, err = r.Describe(ctx, "leaf_001")
	if err != nil {
		t.Fatalf("Describe leaf: %v", err)
	}
	if desc.Kind != KindLeaf {
		t.Errorf("Kind = %q, want %q", desc.Kind, KindLeaf)
	}
	if len(desc.ParentIDs) != 1 || desc.ParentIDs[0] != "cond_001" {
		t.Errorf("ParentIDs = %v, want [cond_001]", desc.ParentIDs)
	}
	if desc.EarliestAt == nil {
		t.Error("EarliestAt should not be nil")
	}
	if desc.LatestAt == nil {
		t.Error("LatestAt should not be nil")
	}
}

func TestDescribe_NotFound(t *testing.T) {
	r, _, _ := setupRetrievalTest(t)
	ctx := context.Background()

	_, err := r.Describe(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent summary")
	}
}

func TestExpand_LeafSummary(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{
		"first message content",
		"second message content",
		"third message content",
	})
	seedLeafSummary(t, q, convID, "leaf_001", "leaf summary", msgIDs)

	result, err := r.Expand(ctx, "leaf_001", 0) // default token cap
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if result.SummaryID != "leaf_001" {
		t.Errorf("SummaryID = %q, want %q", result.SummaryID, "leaf_001")
	}
	if len(result.Messages) != 3 {
		t.Errorf("Messages len = %d, want 3", len(result.Messages))
	}
	if len(result.Children) != 0 {
		t.Errorf("Children len = %d, want 0", len(result.Children))
	}
	if result.Messages[0].Role != RoleUser {
		t.Errorf("Messages[0].Role = %q, want %q", result.Messages[0].Role, RoleUser)
	}
	if result.Messages[0].Content != "first message content" {
		t.Errorf("Messages[0].Content = %q, want %q", result.Messages[0].Content, "first message content")
	}
}

func TestExpand_CondensedSummary(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{"m1", "m2", "m3", "m4"})
	seedLeafSummary(t, q, convID, "leaf_001", "first leaf", msgIDs[:2])
	seedLeafSummary(t, q, convID, "leaf_002", "second leaf", msgIDs[2:])
	seedCondensedSummary(t, q, convID, "cond_001", "condensed summary", 1, []string{"leaf_001", "leaf_002"})

	result, err := r.Expand(ctx, "cond_001", 0)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(result.Children) != 2 {
		t.Fatalf("Children len = %d, want 2", len(result.Children))
	}
	if len(result.Messages) != 0 {
		t.Errorf("Messages len = %d, want 0", len(result.Messages))
	}
	if result.Children[0].SummaryID != "leaf_001" {
		t.Errorf("Children[0].SummaryID = %q, want %q", result.Children[0].SummaryID, "leaf_001")
	}
	if result.Children[0].Kind != KindLeaf {
		t.Errorf("Children[0].Kind = %q, want %q", result.Children[0].Kind, KindLeaf)
	}
}

func TestExpand_TokenCap_Messages(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	// Each message ~100 tokens (400 chars / 4).
	longContent := strings.Repeat("a", 400)
	msgIDs := seedMessages(t, q, convID, []string{longContent, longContent, longContent})
	seedLeafSummary(t, q, convID, "leaf_001", "leaf summary", msgIDs)

	// Token cap that fits only ~1.5 messages (150 tokens).
	result, err := r.Expand(ctx, "leaf_001", 150)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// First message is always included, second exceeds cap.
	if len(result.Messages) != 1 {
		t.Errorf("Messages len = %d, want 1 (token cap hit)", len(result.Messages))
	}
}

func TestExpand_TokenCap_Children(t *testing.T) {
	r, q, convID := setupRetrievalTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{"m1", "m2", "m3", "m4", "m5", "m6"})

	// Each leaf summary content ~100 tokens.
	longSummary := strings.Repeat("b", 400)
	seedLeafSummary(t, q, convID, "leaf_001", longSummary, msgIDs[:2])
	seedLeafSummary(t, q, convID, "leaf_002", longSummary, msgIDs[2:4])
	seedLeafSummary(t, q, convID, "leaf_003", longSummary, msgIDs[4:])
	seedCondensedSummary(t, q, convID, "cond_001", "condensed", 1, []string{"leaf_001", "leaf_002", "leaf_003"})

	// Token cap for ~1.5 children (150 tokens).
	result, err := r.Expand(ctx, "cond_001", 150)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(result.Children) != 1 {
		t.Errorf("Children len = %d, want 1 (token cap hit)", len(result.Children))
	}
}

func TestExpand_NotFound(t *testing.T) {
	r, _, _ := setupRetrievalTest(t)
	ctx := context.Background()

	_, err := r.Expand(ctx, "nonexistent", 0)
	if err == nil {
		t.Fatal("expected error for nonexistent summary")
	}
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncate ascii", "hello world", 5, "hello..."},
		{"truncate multibyte", "hello\u4e16\u754c", 6, "hello\u4e16..."},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateUTF8(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestParseNullTime(t *testing.T) {
	t.Run("valid time", func(t *testing.T) {
		ns := sql.NullString{String: "2025-01-01 10:00:00", Valid: true}
		got := parseNullTime(ns)
		if got == nil {
			t.Fatal("expected non-nil time")
		}
		if got.Year() != 2025 || got.Month() != 1 || got.Day() != 1 {
			t.Errorf("got %v", got)
		}
	})

	t.Run("null", func(t *testing.T) {
		ns := sql.NullString{Valid: false}
		got := parseNullTime(ns)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		ns := sql.NullString{String: "", Valid: true}
		got := parseNullTime(ns)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("unparseable", func(t *testing.T) {
		ns := sql.NullString{String: "not-a-date", Valid: true}
		got := parseNullTime(ns)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}
