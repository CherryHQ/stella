package feishu

import (
	"strings"
	"testing"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
)

func TestIsSkipResponseExact(t *testing.T) {
	if !isSkipResponse("[SKIP]") {
		t.Error("[SKIP] should be a skip response")
	}
}

func TestIsSkipResponseTrimmed(t *testing.T) {
	if !isSkipResponse("  [SKIP]  ") {
		t.Error("whitespace-padded [SKIP] should be a skip response")
	}
}

func TestIsSkipResponseFalse(t *testing.T) {
	cases := []string{
		"hello",
		"[SKIP] extra",
		"extra [SKIP]",
		"[skip]",
		"SKIP",
		"",
	}
	for _, c := range cases {
		if isSkipResponse(c) {
			t.Errorf("%q should not be a skip response", c)
		}
	}
}

func TestGroupBasePromptNoCustom(t *testing.T) {
	got := groupBasePrompt("")
	if got != groupSKIPInstruction {
		t.Errorf("no custom prompt should return just the SKIP instruction, got %q", got)
	}
}

func TestGroupBasePromptWithCustom(t *testing.T) {
	got := groupBasePrompt("Be brief.")
	if !strings.HasPrefix(got, groupSKIPInstruction) {
		t.Error("should start with SKIP instruction")
	}
	if !strings.Contains(got, "Be brief.") {
		t.Error("should contain custom prompt")
	}
}

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
