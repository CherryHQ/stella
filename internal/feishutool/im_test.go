package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestIMToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	def := tool.Definition()
	if def.Name != "feishu_im" {
		t.Fatalf("expected name feishu_im, got %q", def.Name)
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
		"send_message":    true,
		"reply_message":   true,
		"read_messages":   true,
		"get_message":     true,
		"forward_message": true,
		"add_reaction":    true,
		"remove_reaction": true,
	}
	for _, v := range enumVals {
		delete(expected, v.(string))
	}
	if len(expected) > 0 {
		t.Fatalf("missing actions in enum: %v", expected)
	}
}

func TestIMToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestIMToolSendMessageMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	// Missing receive_id_type.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":   "send_message",
		"msg_type": "text",
		"content":  `{"text":"hello"}`,
	})
	if err == nil {
		t.Fatal("expected error for missing receive_id_type")
	}

	// Missing content.
	_, err = tool.Execute(context.Background(), map[string]any{
		"action":          "send_message",
		"receive_id_type": "chat_id",
		"receive_id":      "oc_test",
		"msg_type":        "text",
	})
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestIMToolReplyMessageMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	// Missing message_id.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":   "reply_message",
		"msg_type": "text",
		"content":  `{"text":"hello"}`,
	})
	if err == nil {
		t.Fatal("expected error for missing message_id")
	}
}

func TestIMToolReadMessagesMissingContainer(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "read_messages",
	})
	if err == nil {
		t.Fatal("expected error for missing container_id")
	}
}

func TestIMToolGetMessageMissingID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_message",
	})
	if err == nil {
		t.Fatal("expected error for missing message_id")
	}
}

func TestIMToolForwardMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "forward_message",
	})
	if err == nil {
		t.Fatal("expected error for missing message_id")
	}

	_, err = tool.Execute(context.Background(), map[string]any{
		"action":     "forward_message",
		"message_id": "om_test",
	})
	if err == nil {
		t.Fatal("expected error for missing receive_id")
	}
}

func TestIMToolAddReactionMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":        "add_reaction",
		"reaction_type": "THUMBSUP",
	})
	if err == nil {
		t.Fatal("expected error for missing message_id")
	}

	_, err = tool.Execute(context.Background(), map[string]any{
		"action":     "add_reaction",
		"message_id": "om_test",
	})
	if err == nil {
		t.Fatal("expected error for missing reaction_type")
	}
}

func TestIMToolRemoveReactionMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewIMTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action":      "remove_reaction",
		"reaction_id": "test_id",
	})
	if err == nil {
		t.Fatal("expected error for missing message_id")
	}

	_, err = tool.Execute(context.Background(), map[string]any{
		"action":     "remove_reaction",
		"message_id": "om_test",
	})
	if err == nil {
		t.Fatal("expected error for missing reaction_id")
	}
}
