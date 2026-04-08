package channel

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/chatroute"
)

func TestHandleCommandHelp(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/help", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandStart(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/start", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandWhoami(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/whoami", "SENDER_123")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != "Your ID: SENDER_123" {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "hello world", "user1")
	if ok {
		t.Error("regular text should not be handled")
	}
}

func TestHandleCommandEmpty(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "", "user1")
	if ok {
		t.Error("empty text should not be handled")
	}
}

func TestHandleCommandModel(t *testing.T) {
	rc := &chatroute.ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "/model gpt-4", "user1")
	if ok {
		t.Error("/model should NOT be handled (left to channels)")
	}
}

func TestParseModelArgs(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"3", "3"},
		{"claude", "claude"},
		{" gpt ", "gpt"},
		{" openai/gpt-4 ", "openai/gpt-4"},
	}
	for _, tt := range tests {
		got := ParseModelArgs(tt.input)
		if got != tt.want {
			t.Errorf("ParseModelArgs(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIndexModels(t *testing.T) {
	models := []ModelOption{
		{Provider: "a", Model: "1"},
		{Provider: "b", Model: "2"},
	}
	indexed := IndexModels(models)
	if len(indexed) != 2 {
		t.Fatalf("len = %d, want 2", len(indexed))
	}
	if indexed[0].GlobalIdx != 1 || indexed[1].GlobalIdx != 2 {
		t.Errorf("indices = %d, %d; want 1, 2", indexed[0].GlobalIdx, indexed[1].GlobalIdx)
	}
}

func TestFilterModels(t *testing.T) {
	models := []ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-3.5"},
	}
	filtered := FilterModels(models, "gpt")
	if len(filtered) != 2 {
		t.Fatalf("len = %d, want 2", len(filtered))
	}
	if filtered[0].GlobalIdx != 1 || filtered[1].GlobalIdx != 3 {
		t.Errorf("indices = %d, %d; want 1, 3", filtered[0].GlobalIdx, filtered[1].GlobalIdx)
	}
}
