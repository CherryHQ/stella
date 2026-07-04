package delegate

import (
	"testing"
)

// TestParseDelegateTasks_DuplicateSessionIDRejected verifies CR-010 regression:
// two tasks with the same non-empty session_id must be rejected before goroutines start.
func TestParseDelegateTasks_DuplicateSessionIDRejected(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "t1", "task": "first", "session_id": "shared-sess"},
			map[string]any{"id": "t2", "task": "second", "session_id": "shared-sess"},
		},
	}
	_, err := parseDelegateTasks(args)
	if err == nil {
		t.Fatal("expected error for duplicate session_id, got nil")
	}
}

// TestParseDelegateTasks_EmptySessionIDsAllowed verifies that tasks without session_id
// do not trigger the duplicate check.
func TestParseDelegateTasks_EmptySessionIDsAllowed(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "t1", "task": "first"},
			map[string]any{"id": "t2", "task": "second"},
		},
	}
	tasks, err := parseDelegateTasks(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}

// TestParseDelegateTasks_DistinctSessionIDsAllowed verifies tasks with different
// session_ids are accepted.
func TestParseDelegateTasks_DistinctSessionIDsAllowed(t *testing.T) {
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "t1", "task": "first", "session_id": "sess-a"},
			map[string]any{"id": "t2", "task": "second", "session_id": "sess-b"},
		},
	}
	tasks, err := parseDelegateTasks(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}
