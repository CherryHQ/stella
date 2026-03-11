package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/lcm"
)

// setupGrepTest creates a test DB, conversation, and GrepTool.
func setupGrepTest(t *testing.T) (*GrepTool, *lcm.Queries, int64) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := lcm.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := lcm.New(db)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, lcm.CreateConversationParams{
		SessionID: "sess-grep-test",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	retrieval := lcm.NewRetrievalEngine(q)
	tool := NewGrepTool(retrieval, conv.ID)

	return tool, q, conv.ID
}

func TestGrepDefinition(t *testing.T) {
	tool := NewGrepTool(nil, 0)
	def := tool.Definition()

	if def.Name != "memory_grep" {
		t.Errorf("Name = %q, want %q", def.Name, "memory_grep")
	}
	if def.Description == "" {
		t.Error("Description should not be empty")
	}

	schema, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing properties")
	}
	for _, key := range []string{"pattern", "scope", "limit"} {
		if _, exists := schema[key]; !exists {
			t.Errorf("InputSchema missing property %q", key)
		}
	}

	required, ok := def.InputSchema["required"].([]string)
	if !ok {
		t.Fatal("InputSchema missing required")
	}
	if len(required) != 1 || required[0] != "pattern" {
		t.Errorf("required = %v, want [pattern]", required)
	}
}

func TestGrepExecute_WithResults(t *testing.T) {
	tool, q, convID := setupGrepTest(t)
	ctx := context.Background()

	seedMessages(t, q, convID, []string{
		"implement authentication module",
		"fix database connection pooling",
		"add authentication tests",
	})

	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "authentication",
		"scope":   "messages",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var results []lcm.GrepResult
	if err := json.Unmarshal([]byte(result), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.SourceType != "message" {
			t.Errorf("SourceType = %q, want %q", r.SourceType, "message")
		}
		if !strings.Contains(r.Content, "authentication") {
			t.Errorf("Content %q does not contain 'authentication'", r.Content)
		}
	}
}

func TestGrepExecute_BothScope(t *testing.T) {
	tool, q, convID := setupGrepTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{
		"implement auth module",
		"unrelated content",
	})
	seedLeafSummary(t, q, convID, "sum_001", "summary about auth system", msgIDs)

	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "auth",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var results []lcm.GrepResult
	if err := json.Unmarshal([]byte(result), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	hasMsg, hasSum := false, false
	for _, r := range results {
		if r.SourceType == "message" {
			hasMsg = true
		}
		if r.SourceType == "summary" {
			hasSum = true
		}
	}
	if !hasMsg || !hasSum {
		t.Errorf("expected both message and summary results, got msg=%v sum=%v", hasMsg, hasSum)
	}
}

func TestGrepExecute_NoResults(t *testing.T) {
	tool, q, convID := setupGrepTest(t)
	ctx := context.Background()

	seedMessages(t, q, convID, []string{"hello world"})

	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "nonexistent",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "No matches found." {
		t.Errorf("result = %q, want %q", result, "No matches found.")
	}
}

func TestGrepExecute_MissingPattern(t *testing.T) {
	tool := NewGrepTool(nil, 0)
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error = %q, want to contain 'pattern is required'", err.Error())
	}
}

func TestGrepExecute_EmptyPattern(t *testing.T) {
	tool := NewGrepTool(nil, 0)
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{"pattern": ""})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error = %q, want to contain 'pattern is required'", err.Error())
	}
}

func TestGrepExecute_WithLimit(t *testing.T) {
	tool, q, convID := setupGrepTest(t)
	ctx := context.Background()

	seedMessages(t, q, convID, []string{
		"searchable item one",
		"searchable item two",
		"searchable item three",
	})

	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "searchable",
		"scope":   "messages",
		"limit":   float64(2), // JSON numbers are float64
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var results []lcm.GrepResult
	if err := json.Unmarshal([]byte(result), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
}
