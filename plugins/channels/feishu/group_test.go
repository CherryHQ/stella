package feishu

import (
	"testing"

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
