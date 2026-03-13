package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/memory"
)

const expandTestSession = "sess-expand-test"

// setupExpandTest creates a test engine, bootstraps a session, and returns
// the ExpandTool, Queries handle, and conversation ID.
func setupExpandTest(t *testing.T) (*ExpandTool, *sqlc.Queries, int64) {
	t.Helper()

	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, expandTestSession)
	tool := NewExpandTool(engine)

	return tool, q, convID
}

func TestExpandTool_Definition(t *testing.T) {
	tool := NewExpandTool(nil)
	def := tool.Definition()

	if def.Name != "memory_expand" {
		t.Errorf("Name = %q, want %q", def.Name, "memory_expand")
	}
	if def.Description == "" {
		t.Error("Description should not be empty")
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing properties")
	}
	if _, ok := props["summary_id"]; !ok {
		t.Error("InputSchema missing summary_id property")
	}
	if _, ok := props["token_cap"]; !ok {
		t.Error("InputSchema missing token_cap property")
	}

	required, ok := def.InputSchema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "summary_id" {
		t.Errorf("required = %v, want [summary_id]", required)
	}
}

func TestExpandTool_Execute_LeafSummary(t *testing.T) {
	tool, q, convID := setupExpandTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{
		"first message content",
		"second message content",
		"third message content",
	})
	seedLeafSummary(t, q, convID, "leaf_001", "leaf summary of three messages", msgIDs)

	out, err := tool.Execute(ctx, map[string]any{
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
	if len(result.Children) != 0 {
		t.Errorf("Children len = %d, want 0", len(result.Children))
	}
	if result.Messages[0].Content != "first message content" {
		t.Errorf("Messages[0].Content = %q, want %q", result.Messages[0].Content, "first message content")
	}
}

func TestExpandTool_Execute_CondensedSummary(t *testing.T) {
	tool, q, convID := setupExpandTest(t)
	ctx := context.Background()

	msgIDs := seedMessages(t, q, convID, []string{"m1", "m2", "m3", "m4"})
	seedLeafSummary(t, q, convID, "leaf_001", "first leaf summary", msgIDs[:2])
	seedLeafSummary(t, q, convID, "leaf_002", "second leaf summary", msgIDs[2:])
	seedCondensedSummary(t, q, convID, "cond_001", "condensed summary", 1, []string{"leaf_001", "leaf_002"})

	out, err := tool.Execute(ctx, map[string]any{
		"summary_id": "cond_001",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result memory.ExpandResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
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
	if result.Children[0].Kind != "leaf" {
		t.Errorf("Children[0].Kind = %q, want %q", result.Children[0].Kind, "leaf")
	}
}

func TestExpandTool_Execute_MissingSummaryID(t *testing.T) {
	tool := NewExpandTool(nil)
	ctx := context.Background()

	// No summary_id at all.
	_, err := tool.Execute(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing summary_id")
	}
	if !strings.Contains(err.Error(), "summary_id is required") {
		t.Errorf("error = %q, want it to contain 'summary_id is required'", err.Error())
	}

	// Empty string summary_id.
	_, err = tool.Execute(ctx, map[string]any{"summary_id": ""})
	if err == nil {
		t.Fatal("expected error for empty summary_id")
	}
}

func TestExpandTool_Execute_NotFound(t *testing.T) {
	tool, _, _ := setupExpandTest(t)
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{
		"summary_id": "nonexistent_summary",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent summary_id")
	}
	if !strings.Contains(err.Error(), "memory_expand") {
		t.Errorf("error = %q, want it to contain 'memory_expand'", err.Error())
	}
}
