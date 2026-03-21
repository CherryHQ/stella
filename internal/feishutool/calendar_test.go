package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestCalendarToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	def := tool.Definition()
	if def.Name != "feishu_calendar" {
		t.Fatalf("expected name feishu_calendar, got %q", def.Name)
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
	if _, ok := props["action"]; !ok {
		t.Fatal("expected action in properties")
	}
	// Verify action enum has all expected values.
	actionProp, _ := props["action"].(map[string]any)
	enumVals, _ := actionProp["enum"].([]any)
	expected := map[string]bool{
		"create_event":  true,
		"list_events":   true,
		"get_event":     true,
		"update_event":  true,
		"delete_event":  true,
		"add_attendees": true,
		"freebusy":      true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestCalendarToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestCalendarToolCreateEventMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	// Missing summary.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":     "create_event",
		"start_time": "2024-01-01T09:00:00+08:00",
		"end_time":   "2024-01-01T10:00:00+08:00",
	})
	if err == nil {
		t.Fatal("expected error for missing summary")
	}

	// Missing start_time.
	_, err = tool.Execute(context.Background(), map[string]any{
		"action":  "create_event",
		"summary": "Test",
	})
	if err == nil {
		t.Fatal("expected error for missing start_time/end_time")
	}
}

func TestCalendarToolCreateEventInvalidTime(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":     "create_event",
		"summary":    "Test",
		"start_time": "not-a-time",
		"end_time":   "2024-01-01T10:00:00+08:00",
	})
	if err == nil {
		t.Fatal("expected error for invalid time")
	}
}

func TestCalendarToolGetEventMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_event",
	})
	if err == nil {
		t.Fatal("expected error for missing event_id")
	}
}

func TestCalendarToolDeleteEventMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "delete_event",
	})
	if err == nil {
		t.Fatal("expected error for missing event_id")
	}
}

func TestCalendarToolListEventsMissingTime(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "list_events",
	})
	if err == nil {
		t.Fatal("expected error for missing start_time/end_time")
	}
}

func TestCalendarToolFreebusyMissingFields(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	// Missing user_ids.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":     "freebusy",
		"start_time": "2024-01-01T09:00:00+08:00",
		"end_time":   "2024-01-01T18:00:00+08:00",
	})
	if err == nil {
		t.Fatal("expected error for missing user_ids")
	}
}

func TestCalendarToolAddAttendeesMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewCalendarTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "add_attendees",
	})
	if err == nil {
		t.Fatal("expected error for missing event_id")
	}
}

func TestBuildAttendeeList(t *testing.T) {
	ctx := WithOpenID(context.Background(), "ou_context_user")
	args := map[string]any{
		"attendees": []any{
			map[string]any{"type": "user", "id": "ou_user1"},
			map[string]any{"type": "third_party", "id": "test@example.com"},
		},
	}

	attendees := buildAttendeeList(ctx, args)
	// Should have 3: ou_user1, test@example.com, ou_context_user (from context).
	if len(attendees) != 3 {
		t.Fatalf("expected 3 attendees, got %d", len(attendees))
	}
}

func TestBuildAttendeeListDedup(t *testing.T) {
	ctx := WithOpenID(context.Background(), "ou_user1")
	args := map[string]any{
		"attendees": []any{
			map[string]any{"type": "user", "id": "ou_user1"},
		},
	}

	attendees := buildAttendeeList(ctx, args)
	// Should not duplicate ou_user1.
	if len(attendees) != 1 {
		t.Fatalf("expected 1 attendee (dedup), got %d", len(attendees))
	}
}

func TestBuildCalendarEvent(t *testing.T) {
	args := map[string]any{
		"description":      "Test desc",
		"visibility":       "public",
		"free_busy_status": "free",
		"recurrence":       "FREQ=DAILY;INTERVAL=1",
		"location":         map[string]any{"name": "Office", "address": "123 St"},
		"reminders":        []any{map[string]any{"minutes": float64(15)}},
	}

	event := buildCalendarEvent(args, "Test Event", 1704067200, 1704070800)
	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Summary == nil || *event.Summary != "Test Event" {
		t.Fatal("expected summary to be set")
	}
	if event.Description == nil || *event.Description != "Test desc" {
		t.Fatal("expected description to be set")
	}
	if event.Visibility == nil || *event.Visibility != "public" {
		t.Fatal("expected visibility to be public")
	}
}

func TestBuildCalendarEventPatch(t *testing.T) {
	args := map[string]any{
		"summary":    "Updated Title",
		"start_time": "2024-01-01T09:00:00+08:00",
	}

	event := buildCalendarEventPatch(args)
	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Summary == nil || *event.Summary != "Updated Title" {
		t.Fatal("expected summary to be set")
	}
	if event.StartTime == nil {
		t.Fatal("expected start_time to be set")
	}
}
