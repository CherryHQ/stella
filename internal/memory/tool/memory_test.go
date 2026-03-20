package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
)

// ---------- Definition ----------

func TestMemoryTool_Definition(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	def := mt.Definition()

	if def.Name != "memory" {
		t.Errorf("Name = %q, want %q", def.Name, "memory")
	}
	if def.Description == "" {
		t.Error("Description should not be empty")
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing properties")
	}
	for _, key := range []string{"action", "pattern", "scope", "limit", "summary_id", "token_cap", "content"} {
		if _, exists := props[key]; !exists {
			t.Errorf("InputSchema missing property %q", key)
		}
	}
}

func TestMemoryTool_UnknownAction(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	_, err := mt.Execute(context.Background(), map[string]any{"action": "nope"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error = %q, want to contain 'unknown action'", err.Error())
	}
}

// ---------- Grep ----------

func TestMemoryTool_Grep_WithResults(t *testing.T) {
	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, "sess-grep-test")
	ctx := memory.WithSessionID(context.Background(), "sess-grep-test")
	mt := NewMemoryTool(engine, nil)

	seedMessages(t, q, convID, []string{
		"implement authentication module",
		"fix database connection pooling",
		"add authentication tests",
	})

	result, err := mt.Execute(ctx, map[string]any{
		"action":  "grep",
		"pattern": "authentication",
		"scope":   "messages",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var results []memory.GrepResult
	if err := json.Unmarshal([]byte(result), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if !strings.Contains(r.Content, "authentication") {
			t.Errorf("Content %q does not contain 'authentication'", r.Content)
		}
	}
}

func TestMemoryTool_Grep_NoResults(t *testing.T) {
	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, "sess-grep-noresults")
	ctx := memory.WithSessionID(context.Background(), "sess-grep-noresults")
	mt := NewMemoryTool(engine, nil)

	seedMessages(t, q, convID, []string{"hello world"})

	result, err := mt.Execute(ctx, map[string]any{
		"action":  "grep",
		"pattern": "nonexistent",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "No matches found." {
		t.Errorf("result = %q, want %q", result, "No matches found.")
	}
}

func TestMemoryTool_Grep_MissingPattern(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	_, err := mt.Execute(context.Background(), map[string]any{"action": "grep"})
	if err == nil {
		t.Fatal("expected error for missing pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error = %q, want to contain 'pattern is required'", err.Error())
	}
}

func TestMemoryTool_Grep_NoSessionContext(t *testing.T) {
	engine, _ := setupEngine(t)
	mt := NewMemoryTool(engine, nil)
	_, err := mt.Execute(context.Background(), map[string]any{"action": "grep", "pattern": "test"})
	if err == nil {
		t.Fatal("expected error for missing session context")
	}
	if !strings.Contains(err.Error(), "no session context") {
		t.Errorf("error = %q, want to contain 'no session context'", err.Error())
	}
}

func TestMemoryTool_Grep_WithLimit(t *testing.T) {
	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, "sess-grep-limit")
	ctx := memory.WithSessionID(context.Background(), "sess-grep-limit")
	mt := NewMemoryTool(engine, nil)

	seedMessages(t, q, convID, []string{
		"searchable item one",
		"searchable item two",
		"searchable item three",
	})

	result, err := mt.Execute(ctx, map[string]any{
		"action":  "grep",
		"pattern": "searchable",
		"scope":   "messages",
		"limit":   float64(2),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var results []memory.GrepResult
	if err := json.Unmarshal([]byte(result), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
}

// ---------- Describe ----------

func TestMemoryTool_Describe_Condensed(t *testing.T) {
	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, "sess-describe-test")
	mt := NewMemoryTool(engine, nil)

	msgIDs := seedMessages(t, q, convID, []string{"msg one", "msg two", "msg three"})
	seedLeafSummary(t, q, convID, "leaf_001", "first leaf summary", msgIDs[:2])
	seedLeafSummary(t, q, convID, "leaf_002", "second leaf summary", msgIDs[2:])
	seedCondensedSummary(t, q, convID, "cond_001", "condensed summary", 1, []string{"leaf_001", "leaf_002"})

	result, err := mt.Execute(context.Background(), map[string]any{
		"action":     "describe",
		"summary_id": "cond_001",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var desc memory.DescribeResult
	if err := json.Unmarshal([]byte(result), &desc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if desc.SummaryID != "cond_001" {
		t.Errorf("SummaryID = %q, want %q", desc.SummaryID, "cond_001")
	}
	if desc.Kind != "condensed" {
		t.Errorf("Kind = %q, want %q", desc.Kind, "condensed")
	}
	if len(desc.ChildIDs) != 2 {
		t.Errorf("ChildIDs len = %d, want 2", len(desc.ChildIDs))
	}
}

func TestMemoryTool_Describe_MissingSummaryID(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	_, err := mt.Execute(context.Background(), map[string]any{"action": "describe"})
	if err == nil {
		t.Fatal("expected error for missing summary_id")
	}
	if !strings.Contains(err.Error(), "summary_id is required") {
		t.Errorf("error = %q, want to contain 'summary_id is required'", err.Error())
	}
}

// ---------- Expand ----------

func TestMemoryTool_Expand_LeafSummary(t *testing.T) {
	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, "sess-expand-test")
	mt := NewMemoryTool(engine, nil)

	msgIDs := seedMessages(t, q, convID, []string{
		"first message content",
		"second message content",
		"third message content",
	})
	seedLeafSummary(t, q, convID, "leaf_001", "leaf summary of three messages", msgIDs)

	out, err := mt.Execute(context.Background(), map[string]any{
		"action":     "expand",
		"summary_id": "leaf_001",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result memory.ExpandResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.SummaryID != "leaf_001" {
		t.Errorf("SummaryID = %q, want %q", result.SummaryID, "leaf_001")
	}
	if len(result.Messages) != 3 {
		t.Errorf("Messages len = %d, want 3", len(result.Messages))
	}
}

func TestMemoryTool_Expand_MissingSummaryID(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	_, err := mt.Execute(context.Background(), map[string]any{"action": "expand"})
	if err == nil {
		t.Fatal("expected error for missing summary_id")
	}
	if !strings.Contains(err.Error(), "summary_id is required") {
		t.Errorf("error = %q, want to contain 'summary_id is required'", err.Error())
	}
}

// ---------- User Memory Update ----------

func setupUserMemoryContext(t *testing.T) (*MemoryTool, *memory.UserMemoryStore, int64, context.Context) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	var userID int64 = 42

	memStore := memory.NewUserMemoryStore(store)
	mt := NewMemoryTool(nil, memStore)

	ctx := memory.WithUserID(context.Background(), userID)
	ctx = memory.WithAgentID(ctx, "anna")

	return mt, memStore, userID, ctx
}

func TestMemoryTool_UserMemoryUpdate(t *testing.T) {
	mt, memStore, userID, ctx := setupUserMemoryContext(t)

	result, err := mt.Execute(ctx, map[string]any{
		"action":  "user_memory_update",
		"content": "## User Preferences\nPrefers concise responses\n\n## About the User\nGo developer",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	content, err := memStore.Get(ctx, userID, "anna")
	if err != nil {
		t.Fatalf("memStore.Get: %v", err)
	}
	if content != "## User Preferences\nPrefers concise responses\n\n## About the User\nGo developer" {
		t.Errorf("stored content = %q", content)
	}
}

func TestMemoryTool_UserMemoryUpdate_RequiresContent(t *testing.T) {
	mt, _, _, ctx := setupUserMemoryContext(t)
	_, err := mt.Execute(ctx, map[string]any{"action": "user_memory_update"})
	if err == nil {
		t.Error("expected error for missing content")
	}
}

func TestMemoryTool_UserMemoryUpdate_NoUserContext(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	_, err := mt.Execute(context.Background(), map[string]any{
		"action":  "user_memory_update",
		"content": "test",
	})
	if err == nil {
		t.Fatal("expected error for missing user context")
	}
	if !strings.Contains(err.Error(), "no user context") {
		t.Errorf("error = %q, want to contain 'no user context'", err.Error())
	}
}

func TestMemoryTool_UserMemoryUpdate_NoAgentContext(t *testing.T) {
	mt := NewMemoryTool(nil, nil)
	ctx := memory.WithUserID(context.Background(), 1)
	_, err := mt.Execute(ctx, map[string]any{
		"action":  "user_memory_update",
		"content": "test",
	})
	if err == nil {
		t.Fatal("expected error for missing agent context")
	}
	if !strings.Contains(err.Error(), "no agent context") {
		t.Errorf("error = %q, want to contain 'no agent context'", err.Error())
	}
}
