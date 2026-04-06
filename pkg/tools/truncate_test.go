package tools

import (
	"strings"
	"testing"
)

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{}},
		{"a\nb\nc\n", []string{"a\n", "b\n", "c\n"}},
		{"a\nb\nc", []string{"a\n", "b\n", "c"}},
		{"single", []string{"single"}},
	}
	for _, tc := range tests {
		got := SplitLines(tc.input)
		// Handle empty slice vs nil
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("SplitLines(%q): got %d lines, want %d", tc.input, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("SplitLines(%q)[%d]: got %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestTruncateHead_NoTruncation(t *testing.T) {
	text := "hello\nworld\n"
	result := TruncateHead(text)
	if result.Content != text {
		t.Errorf("expected no truncation, got %q", result.Content)
	}
	if result.OutputLines != 2 {
		t.Errorf("expected 2 output lines, got %d", result.OutputLines)
	}
}

func TestTruncateTail_NoTruncation(t *testing.T) {
	text := "line1\nline2\n"
	result := TruncateTail(text)
	if result.Content != text {
		t.Errorf("expected no truncation, got %q", result.Content)
	}
}

func TestTruncateHead_LargeInput(t *testing.T) {
	// Build input larger than defaults (>2000 lines).
	var sb strings.Builder
	for i := 0; i < 2500; i++ {
		sb.WriteString("line\n")
	}
	input := sb.String()

	result := TruncateHead(input)
	if result.OutputLines > defaultMaxLines {
		t.Errorf("expected at most %d lines, got %d", defaultMaxLines, result.OutputLines)
	}
	if !strings.Contains(result.Content, "[Output truncated") {
		t.Error("expected truncation header")
	}
}

func TestTruncateTail_LargeInput(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 2500; i++ {
		sb.WriteString("line\n")
	}
	input := sb.String()

	result := TruncateTail(input)
	if result.OutputLines > defaultMaxLines {
		t.Errorf("expected at most %d lines, got %d", defaultMaxLines, result.OutputLines)
	}
	if !strings.Contains(result.Content, "last") {
		t.Error("expected 'last' in truncation header")
	}
}

func TestMaxLinesEnvVar(t *testing.T) {
	t.Setenv("ANNA_TOOL_MAX_LINES", "100")
	got := maxLines()
	if got != 100 {
		t.Errorf("expected 100 from env, got %d", got)
	}
}

func TestMaxLinesDefault(t *testing.T) {
	t.Setenv("ANNA_TOOL_MAX_LINES", "")
	got := maxLines()
	if got != defaultMaxLines {
		t.Errorf("expected default %d, got %d", defaultMaxLines, got)
	}
}

func TestMaxBytesEnvVar(t *testing.T) {
	t.Setenv("ANNA_TOOL_MAX_BYTES", "1024")
	got := maxBytes()
	if got != 1024 {
		t.Errorf("expected 1024 from env, got %d", got)
	}
}

func TestMaxBytesDefault(t *testing.T) {
	t.Setenv("ANNA_TOOL_MAX_BYTES", "")
	got := maxBytes()
	if got != defaultMaxBytes {
		t.Errorf("expected default %d, got %d", defaultMaxBytes, got)
	}
}

func TestMaxLinesInvalidEnvVar(t *testing.T) {
	t.Setenv("ANNA_TOOL_MAX_LINES", "notanumber")
	got := maxLines()
	if got != defaultMaxLines {
		t.Errorf("expected default for invalid env, got %d", got)
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"plain text", "hello world\nfoo bar\n", false},
		{"null byte", "binary\x00data", true},
		{"valid utf8", "日本語テスト\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsBinary(tc.input)
			if got != tc.want {
				t.Errorf("IsBinary(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
