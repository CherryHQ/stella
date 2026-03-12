package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vaayne/anna/memory"
)

const describeTestSession = "sess-describe-test"

// setupDescribeTest creates a test engine with seeded data and returns a DescribeTool.
func setupDescribeTest(t *testing.T) *DescribeTool {
	t.Helper()

	engine, q := setupEngine(t)
	convID := bootstrapSession(t, engine, q, describeTestSession)

	msgIDs := seedMessages(t, q, convID, []string{"msg one", "msg two", "msg three"})
	seedLeafSummary(t, q, convID, "leaf_001", "first leaf summary", msgIDs[:2])
	seedLeafSummary(t, q, convID, "leaf_002", "second leaf summary", msgIDs[2:])
	seedCondensedSummary(t, q, convID, "cond_001", "condensed summary", 1, []string{"leaf_001", "leaf_002"})

	return NewDescribeTool(engine)
}

func TestDescribeDefinition(t *testing.T) {
	tool := NewDescribeTool(nil)
	def := tool.Definition()

	if def.Name != "memory_describe" {
		t.Errorf("Name = %q, want %q", def.Name, "memory_describe")
	}
	if def.Description == "" {
		t.Error("Description should not be empty")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing properties")
	}
	if _, ok := props["summary_id"]; !ok {
		t.Error("InputSchema missing summary_id property")
	}

	required, ok := def.InputSchema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "summary_id" {
		t.Errorf("required = %v, want [summary_id]", required)
	}
}

func TestDescribeExecute_ValidCondensed(t *testing.T) {
	tool := setupDescribeTest(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]any{"summary_id": "cond_001"})
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
	if desc.Depth != 1 {
		t.Errorf("Depth = %d, want 1", desc.Depth)
	}
	if len(desc.ChildIDs) != 2 {
		t.Errorf("ChildIDs len = %d, want 2", len(desc.ChildIDs))
	}
}

func TestDescribeExecute_LeafWithParent(t *testing.T) {
	tool := setupDescribeTest(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]any{"summary_id": "leaf_001"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var desc memory.DescribeResult
	if err := json.Unmarshal([]byte(result), &desc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if desc.Kind != "leaf" {
		t.Errorf("Kind = %q, want %q", desc.Kind, "leaf")
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

func TestDescribeExecute_MissingSummaryID(t *testing.T) {
	tool := NewDescribeTool(nil)

	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing summary_id")
	}
}

func TestDescribeExecute_EmptySummaryID(t *testing.T) {
	tool := NewDescribeTool(nil)

	_, err := tool.Execute(context.Background(), map[string]any{"summary_id": ""})
	if err == nil {
		t.Fatal("expected error for empty summary_id")
	}
}

func TestDescribeExecute_Nonexistent(t *testing.T) {
	tool := setupDescribeTest(t)
	ctx := context.Background()

	_, err := tool.Execute(ctx, map[string]any{"summary_id": "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent summary")
	}
}
