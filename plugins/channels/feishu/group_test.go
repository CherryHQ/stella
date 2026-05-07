package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
)

func TestAttributeGroupContentWithCachedName(t *testing.T) {
	bot := &Bot{}
	bot.cacheName("ou_alice", "Alice")
	content := channel.TextContent("hello")
	got := bot.attributeGroupContent("ou_alice", content)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(got))
	}
	prefix, ok := got[0].(ai.TextContent)
	if !ok {
		t.Fatalf("expected TextContent prefix, got %T", got[0])
	}
	if prefix.Text != "[Alice]: " {
		t.Errorf("prefix = %q, want [Alice]: ", prefix.Text)
	}
}

func TestAttributeGroupContentFallbackToOpenID(t *testing.T) {
	bot := &Bot{}
	content := channel.TextContent("hello")
	got := bot.attributeGroupContent("ou_unknown", content)
	if len(got) < 1 {
		t.Fatalf("expected at least 1 block, got %d", len(got))
	}
	prefix, ok := got[0].(ai.TextContent)
	if !ok {
		t.Fatalf("expected TextContent prefix, got %T", got[0])
	}
	if prefix.Text != "[ou_unknown]: " {
		t.Errorf("prefix = %q, want [ou_unknown]: ", prefix.Text)
	}
}

// --- isGroupTrigger ---

func TestIsGroupTriggerDisabled(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "disabled"}}
	// Even with a mention, disabled mode returns false.
	key := "@_user_1"
	mentions := []*larkim.MentionEvent{{Key: &key}}
	if bot.isGroupTrigger("oc_test", mentions) {
		t.Error("disabled mode should always return false")
	}
	if bot.isGroupTrigger("oc_test", nil) {
		t.Error("disabled mode with no mentions should return false")
	}
}

func TestIsGroupTriggerMentionMode(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "mention"}}
	bot.botOpenID.Store("ou_bot123")

	// With bot mention → true.
	openID := "ou_bot123"
	mentions := []*larkim.MentionEvent{{
		Id: &larkim.UserId{OpenId: &openID},
	}}
	if !bot.isGroupTrigger("oc_test", mentions) {
		t.Error("mention mode with bot mention should return true")
	}

	// Without mentions → false.
	if bot.isGroupTrigger("oc_test", nil) {
		t.Error("mention mode without mentions should return false")
	}

	// With non-bot mention → false.
	otherID := "ou_other456"
	otherMentions := []*larkim.MentionEvent{{
		Id: &larkim.UserId{OpenId: &otherID},
	}}
	if bot.isGroupTrigger("oc_test", otherMentions) {
		t.Error("mention mode with non-bot mention should return false")
	}
}

func TestIsGroupTriggerAlwaysMode(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "always"}}
	bot.botOpenID.Store("ou_bot123")

	// "always" mode now behaves as "mention" — only triggers on @mention.
	if bot.isGroupTrigger("oc_test", nil) {
		t.Error("always mode without mention should return false (treated as mention)")
	}

	// With bot mention → true.
	openID := "ou_bot123"
	mentions := []*larkim.MentionEvent{{
		Id: &larkim.UserId{OpenId: &openID},
	}}
	if !bot.isGroupTrigger("oc_test", mentions) {
		t.Error("always mode with bot mention should return true")
	}
}

func TestIsGroupTriggerAlwaysModeWarnsOnce(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "always"}}
	bot.botOpenID.Store("ou_bot123")

	chatID := "oc_warn_test"

	// Before any call, chatID should not be in the warned map.
	if _, ok := bot.alwaysModeWarned.Load(chatID); ok {
		t.Fatal("chatID should not be in alwaysModeWarned before first call")
	}

	// First call populates the warned map.
	bot.isGroupTrigger(chatID, nil)
	if _, ok := bot.alwaysModeWarned.Load(chatID); !ok {
		t.Error("chatID should be in alwaysModeWarned after first call")
	}

	// Second call should not panic and the entry remains.
	bot.isGroupTrigger(chatID, nil)
	if _, ok := bot.alwaysModeWarned.Load(chatID); !ok {
		t.Error("chatID should still be in alwaysModeWarned after second call")
	}

	// A different chatID should not be present.
	if _, ok := bot.alwaysModeWarned.Load("oc_other"); ok {
		t.Error("unrelated chatID should not be in alwaysModeWarned")
	}
}
