package feishutool

import (
	"context"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
)

func TestUserToolDefinition(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewUserTool(client)

	def := tool.Definition()
	if def.Name != "feishu_user" {
		t.Fatalf("expected name feishu_user, got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if def.InputSchema == nil {
		t.Fatal("expected non-nil input schema")
	}

	// Verify schema has required action field.
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in input schema")
	}
	if _, ok := props["action"]; !ok {
		t.Fatal("expected action in properties")
	}
}

func TestUserToolUnknownAction(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewUserTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestUserToolGetUserMissingOpenID(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewUserTool(client)

	// No open_id in args or context.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "get_user",
	})
	if err == nil {
		t.Fatal("expected error for missing open_id")
	}
}

func TestUserToolSearchUserMissingParams(t *testing.T) {
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewUserTool(client)

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "search_user",
	})
	if err == nil {
		t.Fatal("expected error for missing emails/mobiles")
	}
}

func TestUserToolGetUserFallbackToContext(t *testing.T) {
	// This test verifies the context fallback logic for open_id.
	// It will fail at the API call (fake client), but we verify
	// it gets past the open_id validation.
	larkClient := lark.NewClient("fake_id", "fake_secret")
	client := NewClient(larkClient)
	tool := NewUserTool(client)

	ctx := WithOpenID(context.Background(), "ou_context_id")
	_, err := tool.Execute(ctx, map[string]any{
		"action": "get_user",
	})
	// Should fail at the API call, not at validation.
	if err == nil {
		t.Fatal("expected API error, not nil")
	}
	// Should not be the "open_id is required" error.
	if err.Error() == "feishu_user get_user: open_id is required" {
		t.Fatal("should have used context open_id")
	}
}

func TestFormatUser(t *testing.T) {
	name := "Alice"
	openID := "ou_abc"
	email := "alice@example.com"
	city := "Shanghai"

	u := &larkcontact.User{
		Name:   &name,
		OpenId: &openID,
		Email:  &email,
		City:   &city,
	}

	m := formatUser(u)
	if m["name"] != "Alice" {
		t.Fatalf("expected Alice, got %v", m["name"])
	}
	if m["open_id"] != "ou_abc" {
		t.Fatalf("expected ou_abc, got %v", m["open_id"])
	}
	if m["email"] != "alice@example.com" {
		t.Fatalf("expected alice@example.com, got %v", m["email"])
	}
	if m["city"] != "Shanghai" {
		t.Fatalf("expected Shanghai, got %v", m["city"])
	}
}

func TestFormatUserEmpty(t *testing.T) {
	u := &larkcontact.User{}
	m := formatUser(u)
	if len(m) != 0 {
		t.Fatalf("expected empty map for empty user, got %v", m)
	}
}

func TestToStringSlice(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want int
	}{
		{"from []any", map[string]any{"emails": []any{"a@b.com", "c@d.com"}}, "emails", 2},
		{"from []string", map[string]any{"emails": []string{"a@b.com"}}, "emails", 1},
		{"missing key", map[string]any{}, "emails", 0},
		{"wrong type", map[string]any{"emails": 42}, "emails", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toStringSlice(tt.args, tt.key)
			if len(got) != tt.want {
				t.Fatalf("expected %d items, got %d", tt.want, len(got))
			}
		})
	}
}
