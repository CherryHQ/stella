package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vaayne/anna/db/sqlc"
	"github.com/vaayne/anna/memory"
)

const grepTestSession = "sess-grep-test"

// setupGrepTest creates a test engine, bootstraps a session, and returns
// the GrepTool, Queries handle, conversation ID, and a context with session ID.
func setupGrepTest(t *testing.T) (*GrepTool, *sqlc.Queries, int64, context.Context) {
	t.Helper()

	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, grepTestSession)
	ctx := memory.WithSessionID(context.Background(), grepTestSession)
	tool := NewGrepTool(engine)

	return tool, q, convID, ctx
}

func TestGrepDefinition(t *testing.T) {
	tool := NewGrepTool(nil)
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
	tool, q, convID, ctx := setupGrepTest(t)

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

	var results []memory.GrepResult
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
	tool, q, convID, ctx := setupGrepTest(t)

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

	var results []memory.GrepResult
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
	tool, q, convID, ctx := setupGrepTest(t)

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
	tool := NewGrepTool(nil)
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
	tool := NewGrepTool(nil)
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{"pattern": ""})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error = %q, want to contain 'pattern is required'", err.Error())
	}
}

func TestGrepExecute_NoSessionContext(t *testing.T) {
	engine, _ := setupEngine(t)
	tool := NewGrepTool(engine)
	ctx := context.Background() // no session ID in context

	_, err := tool.Execute(ctx, map[string]any{"pattern": "test"})
	if err == nil {
		t.Fatal("expected error for missing session context")
	}
	if !strings.Contains(err.Error(), "no session context") {
		t.Errorf("error = %q, want to contain 'no session context'", err.Error())
	}
}

func TestGrepExecute_WithLimit(t *testing.T) {
	tool, q, convID, ctx := setupGrepTest(t)

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

	var results []memory.GrepResult
	if err := json.Unmarshal([]byte(result), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
}
