package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestChatToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewChatTool(client)

	def := tool.Definition()
	if def.Name != "feishu_chat" {
		t.Fatalf("expected name feishu_chat, got %q", def.Name)
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
		"search_chats":   true,
		"get_chat":       true,
		"list_members":   true,
		"add_members":    true,
		"remove_members": true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestChatToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewChatTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestChatToolSearchMissingQuery(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewChatTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "search_chats",
	})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestChatToolGetChatMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewChatTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_chat",
	})
	if err == nil {
		t.Fatal("expected error for missing chat_id")
	}
}

func TestChatToolAddMembersMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewChatTool(client)

	// Missing chat_id.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":     "add_members",
		"member_ids": []any{"ou_test"},
	})
	if err == nil {
		t.Fatal("expected error for missing chat_id")
	}

	// Missing member_ids.
	_, err = tool.Execute(context.Background(), map[string]any{
		"action":  "add_members",
		"chat_id": "oc_test",
	})
	if err == nil {
		t.Fatal("expected error for missing member_ids")
	}
}

func TestChatToolRemoveMembersMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewChatTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remove_members",
		"chat_id": "oc_test",
	})
	if err == nil {
		t.Fatal("expected error for missing member_ids")
	}
}
