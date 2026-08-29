package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestWithSystemOverride(t *testing.T) {
	ctx := context.Background()

	// empty string is a no-op
	ctx2 := WithSystemOverride(ctx, "")
	if _, ok := SystemOverrideFromContext(ctx2); ok {
		t.Error("expected no override for empty string")
	}

	// set a value
	ctx3 := WithSystemOverride(ctx, "You are helpful.")
	sys, ok := SystemOverrideFromContext(ctx3)
	if !ok || sys != "You are helpful." {
		t.Errorf("SystemOverrideFromContext = %q, %v", sys, ok)
	}

	// nil context
	_, ok = SystemOverrideFromContext(context.Background())
	if ok {
		t.Error("expected false for nil context")
	}
}

func TestWithChannel(t *testing.T) {
	ctx := context.Background()

	ctx2 := WithChannel(ctx, "")
	if _, ok := ChannelFromContext(ctx2); ok {
		t.Error("expected no channel for empty string")
	}

	ctx3 := WithChannel(ctx, "telegram")
	ch, ok := ChannelFromContext(ctx3)
	if !ok || ch != "telegram" {
		t.Errorf("ChannelFromContext = %q, %v", ch, ok)
	}

	_, ok = ChannelFromContext(context.Background())
	if ok {
		t.Error("expected false for nil context")
	}
}

func TestWithExcludedTools(t *testing.T) {
	ctx := context.Background()

	// empty names is a no-op
	ctx2 := WithExcludedTools(ctx)
	if got := ExcludedToolsFromContext(ctx2); len(got) != 0 {
		t.Errorf("expected no excluded tools, got %v", got)
	}

	// deduplication
	ctx3 := WithExcludedTools(ctx, "bash", "read", "bash", "")
	got := ExcludedToolsFromContext(ctx3)
	want := []string{"bash", "read"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExcludedToolsFromContext = %v, want %v", got, want)
	}

	// nil context
	if got := ExcludedToolsFromContext(context.Background()); len(got) != 0 {
		t.Errorf("expected nil for nil context, got %v", got)
	}

	// returns a copy (mutation-safe)
	got[0] = "mutated"
	got2 := ExcludedToolsFromContext(ctx3)
	if got2[0] != "bash" {
		t.Error("ExcludedToolsFromContext should return a defensive copy")
	}
}

func TestSummarizeToolInput(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"bash", map[string]any{"command": "ls -la"}, "ls -la"},
		{"bash", map[string]any{"command": strings.Repeat("x", 90)}, strings.Repeat("x", 80) + "..."},
		{"read", map[string]any{"path": "/foo/bar.go"}, "/foo/bar.go"},
		{"write", map[string]any{"path": "/out.txt"}, "/out.txt"},
		{"edit", map[string]any{"path": "/src.go"}, "/src.go"},
		{"memory_search", map[string]any{"q": "deploy checklist"}, "deploy checklist"},
		{"memory_read", map[string]any{"ref": "mem1.abc"}, "mem1.abc"},
		// This summarizes a live call, so the retired union name carries no
		// meaning here any more: it is a name nothing can emit.
		{"memory", map[string]any{"action": "search"}, ""},
		{"unknown", map[string]any{"path": "/x"}, ""},
		{"bash", map[string]any{}, ""},
	}
	for _, tc := range tests {
		got := summarizeToolInput(tc.tool, tc.args)
		if got != tc.want {
			t.Errorf("summarizeToolInput(%q, %v) = %q, want %q", tc.tool, tc.args, got, tc.want)
		}
	}
}

func TestSummarizeToolResult(t *testing.T) {
	makeResult := func(text string, isError bool) ai.ToolResultMessage {
		return ai.ToolResultMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: text}},
			IsError: isError,
		}
	}

	// empty content
	empty := ai.ToolResultMessage{Content: []ai.ContentBlock{}}
	if got := summarizeToolResult(empty); got != "" {
		t.Errorf("empty: got %q", got)
	}

	// short success: shown inline
	if got := summarizeToolResult(makeResult("ok", false)); got != "ok" {
		t.Errorf("short: got %q", got)
	}

	// multi-line: shows line count
	multiLine := "line1\nline2\nline3"
	got := summarizeToolResult(makeResult(multiLine, false))
	if !strings.Contains(got, "line") {
		t.Errorf("multi-line: got %q", got)
	}

	// long single line: shows char count
	long := strings.Repeat("a", 100)
	got = summarizeToolResult(makeResult(long, false))
	if !strings.Contains(got, "chars") {
		t.Errorf("long: got %q", got)
	}

	// error: truncates to first line
	errResult := makeResult("error message\nmore detail", true)
	got = summarizeToolResult(errResult)
	if got != "error message" {
		t.Errorf("error: got %q", got)
	}

	// error: truncates at 120 chars
	longErr := strings.Repeat("e", 130)
	got = summarizeToolResult(makeResult(longErr, true))
	if !strings.HasSuffix(got, "...") {
		t.Errorf("long error: got %q", got)
	}
}
