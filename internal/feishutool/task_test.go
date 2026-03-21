package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestTaskToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	def := tool.Definition()
	if def.Name != "feishu_task" {
		t.Fatalf("expected name feishu_task, got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if def.InputSchema == nil {
		t.Fatal("expected non-nil input schema")
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in input schema")
	}
	actionProp, _ := props["action"].(map[string]any)
	enumVals, _ := actionProp["enum"].([]any)
	expected := map[string]bool{
		"create_task":     true,
		"list_tasks":      true,
		"get_task":        true,
		"update_task":     true,
		"complete_task":   true,
		"create_tasklist": true,
		"list_tasklists":  true,
		"create_subtask":  true,
		"list_subtasks":   true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestTaskToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestTaskToolCreateTaskMissingSummary(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "create_task",
	})
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestTaskToolGetTaskMissingGUID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_task",
	})
	if err == nil {
		t.Fatal("expected error for missing task_guid")
	}
}

func TestTaskToolUpdateTaskMissingGUID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "update_task",
	})
	if err == nil {
		t.Fatal("expected error for missing task_guid")
	}
}

func TestTaskToolUpdateTaskNoFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":    "update_task",
		"task_guid": "some-guid",
	})
	if err == nil {
		t.Fatal("expected error for no fields to update")
	}
}

func TestTaskToolCompleteTaskMissingGUID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "complete_task",
	})
	if err == nil {
		t.Fatal("expected error for missing task_guid")
	}
}

func TestTaskToolCreateTasklistMissingSummary(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "create_tasklist",
	})
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestTaskToolCreateSubtaskMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	// Missing task_guid.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":  "create_subtask",
		"summary": "Sub",
	})
	if err == nil {
		t.Fatal("expected error for missing task_guid")
	}

	// Missing summary.
	_, err = tool.Execute(context.Background(), map[string]any{
		"action":    "create_subtask",
		"task_guid": "some-guid",
	})
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestTaskToolListSubtasksMissingGUID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewTaskTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "list_subtasks",
	})
	if err == nil {
		t.Fatal("expected error for missing task_guid")
	}
}

func TestBuildInputTask(t *testing.T) {
	args := map[string]any{
		"description":   "desc",
		"repeat_rule":   "FREQ=DAILY",
		"tasklist_guid": "tl_123",
		"due": map[string]any{
			"timestamp":  "2024-01-01T18:00:00+08:00",
			"is_all_day": false,
		},
		"members": []any{
			map[string]any{"id": "ou_user1", "role": "assignee"},
			map[string]any{"id": "ou_user2"}, // default role
		},
	}

	task, err := buildInputTask(args, "Test Task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.Summary == nil || *task.Summary != "Test Task" {
		t.Fatal("expected summary to be set")
	}
	if task.Description == nil || *task.Description != "desc" {
		t.Fatal("expected description to be set")
	}
	if task.Due == nil || task.Due.Timestamp == nil {
		t.Fatal("expected due to be set")
	}
	if len(task.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(task.Members))
	}
	if task.Tasklists == nil || len(task.Tasklists) != 1 {
		t.Fatal("expected 1 tasklist")
	}
}

func TestBuildInputTaskInvalidDue(t *testing.T) {
	args := map[string]any{
		"due": map[string]any{
			"timestamp": "not-a-time",
		},
	}

	_, err := buildInputTask(args, "Test")
	if err == nil {
		t.Fatal("expected error for invalid due timestamp")
	}
}

func TestBuildUpdateTask(t *testing.T) {
	args := map[string]any{
		"summary":     "Updated",
		"description": "New desc",
	}

	task, fields, err := buildUpdateTask(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("expected 2 update fields, got %d", len(fields))
	}
	if task.Summary == nil || *task.Summary != "Updated" {
		t.Fatal("expected summary to be set")
	}
}

func TestBuildTaskMembers(t *testing.T) {
	raw := []any{
		map[string]any{"id": "ou_1", "role": "follower"},
		map[string]any{"id": "ou_2"},    // default role
		map[string]any{"id": ""},        // skipped
		map[string]any{"name": "no id"}, // skipped
	}

	members := buildTaskMembers(raw)
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	if *members[0].Role != "follower" {
		t.Fatalf("expected follower role, got %q", *members[0].Role)
	}
	if *members[1].Role != "assignee" {
		t.Fatalf("expected assignee default role, got %q", *members[1].Role)
	}
}
