package qq

import (
	"strings"
	"testing"

	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/channel"
)

// --- splitMessage ---

func TestSplitMessageShort(t *testing.T) {
	chunks := splitMessage("hello")
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("chunks = %v, want [hello]", chunks)
	}
}

func TestSplitMessageExactLimit(t *testing.T) {
	msg := strings.Repeat("a", qqMaxMessageLen)
	chunks := splitMessage(msg)
	if len(chunks) != 1 {
		t.Errorf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestSplitMessageLong(t *testing.T) {
	msg := strings.Repeat("a", qqMaxMessageLen+100)
	chunks := splitMessage(msg)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != qqMaxMessageLen {
		t.Errorf("chunk[0] len = %d, want %d", len(chunks[0]), qqMaxMessageLen)
	}
	if len(chunks[1]) != 100 {
		t.Errorf("chunk[1] len = %d, want 100", len(chunks[1]))
	}
}

func TestSplitMessageAtNewline(t *testing.T) {
	part1 := strings.Repeat("a", 3000)
	part2 := strings.Repeat("b", 2000)
	msg := part1 + "\n" + part2

	chunks := splitMessage(msg)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0] != part1+"\n" {
		t.Errorf("chunk[0] should end at newline")
	}
}

func TestSplitMessageMultibyteUTF8(t *testing.T) {
	// Build a string of Chinese characters that exceeds the limit.
	// Each character is 3 bytes in UTF-8.
	char := "中"
	msg := strings.Repeat(char, qqMaxMessageLen) // 3*qqMaxMessageLen bytes
	chunks := splitMessage(msg)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	// Verify no chunk starts with a continuation byte (broken rune).
	for i, c := range chunks {
		if len(c) > 0 && c[0]&0xC0 == 0x80 {
			t.Errorf("chunk[%d] starts with UTF-8 continuation byte", i)
		}
	}
}

// --- buildStreamDisplay ---

func TestBuildStreamDisplayShort(t *testing.T) {
	d := buildStreamDisplay("hello", "")
	if !strings.HasSuffix(d, typingCursor) {
		t.Errorf("display should end with cursor, got %q", d)
	}
	if !strings.HasPrefix(d, "hello") {
		t.Errorf("display should start with text, got %q", d)
	}
}

func TestBuildStreamDisplayWithTool(t *testing.T) {
	d := buildStreamDisplay("hello", "⚡ bash: ls")
	if !strings.Contains(d, "⚡ bash: ls") {
		t.Errorf("display should contain tool line, got %q", d)
	}
}

func TestBuildStreamDisplayTruncates(t *testing.T) {
	long := strings.Repeat("x", qqMaxMessageLen+500)
	d := buildStreamDisplay(long, "")
	if len(d) > qqMaxMessageLen {
		t.Errorf("display len = %d, should be <= %d", len(d), qqMaxMessageLen)
	}
	if !strings.Contains(d, "...") {
		t.Errorf("truncated display should contain ellipsis")
	}
}

// --- toolLine ---

func TestToolLineRunning(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "bash", Status: "running", Input: "ls -la"})
	if !strings.Contains(line, "bash") || !strings.Contains(line, "ls -la") {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestToolLineRunningTruncatesMultibyte(t *testing.T) {
	// 61 Chinese characters = well over 60 runes
	input := strings.Repeat("中", 61)
	line := toolLine(&runner.ToolUseEvent{Tool: "bash", Status: "running", Input: input})
	// Should be truncated to 57 runes + "..."
	if !strings.HasSuffix(line, "...") {
		t.Errorf("expected truncation ellipsis, got %q", line)
	}
}

func TestToolLineError(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "read", Status: "error"})
	if !strings.Contains(line, "failed") {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestToolLineDefault(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "bash", Status: "done"})
	if line != "" {
		t.Errorf("expected empty, got %q", line)
	}
}

// --- filterModelsIndexed ---

func TestFilterModelsIndexed(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-3.5"},
	}

	result := filterModelsIndexed(models, "openai")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(result))
	}
	if result[0].globalIdx != 1 || result[1].globalIdx != 3 {
		t.Errorf("global indices should be 1 and 3, got %d and %d", result[0].globalIdx, result[1].globalIdx)
	}
}

func TestFilterModelsIndexedNoMatch(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	result := filterModelsIndexed(models, "gemini")
	if len(result) != 0 {
		t.Errorf("expected 0 matches, got %d", len(result))
	}
}

func TestFilterModelsIndexedCaseInsensitive(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "Anthropic", Model: "Claude-3"},
	}
	result := filterModelsIndexed(models, "CLAUDE")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
}
