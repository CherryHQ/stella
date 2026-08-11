package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
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
	if result.Truncated {
		t.Fatal("expected untruncated result")
	}
}

func TestTruncateTail_NoTruncation(t *testing.T) {
	text := "line1\nline2\n"
	result := TruncateTail(text)
	if result.Content != text {
		t.Errorf("expected no truncation, got %q", result.Content)
	}
	if result.Truncated {
		t.Fatal("expected untruncated result")
	}
}

func TestTruncateHead_LargeInput(t *testing.T) {
	var sb strings.Builder
	for range 2500 {
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
	if result.TruncatedBy != TruncatedByLines {
		t.Fatalf("expected line truncation, got %q", result.TruncatedBy)
	}
	if result.TotalLines != 2500 {
		t.Fatalf("expected 2500 total lines, got %d", result.TotalLines)
	}
}

func TestTruncateTail_LargeInput(t *testing.T) {
	var sb strings.Builder
	for range 2500 {
		sb.WriteString("line\n")
	}
	input := sb.String()

	result := TruncateTail(input)
	if result.OutputLines > defaultMaxLines {
		t.Errorf("expected at most %d lines, got %d", defaultMaxLines, result.OutputLines)
	}
	if !strings.Contains(result.Content, "showing last") {
		t.Error("expected 'showing last' in truncation header")
	}
	if result.TruncatedBy != TruncatedByLines {
		t.Fatalf("expected line truncation, got %q", result.TruncatedBy)
	}
}

func TestTruncateHead_FirstLineExceedsByteLimit(t *testing.T) {
	input := strings.Repeat("x", 10) + "\nrest\n"
	result := truncateHead(input, 2000, 5)

	if !result.Truncated {
		t.Fatal("expected truncation")
	}
	if result.TruncatedBy != TruncatedByBytes {
		t.Fatalf("expected byte truncation, got %q", result.TruncatedBy)
	}
	if !result.FirstLineExceedsLimit {
		t.Fatal("expected firstLineExceedsLimit")
	}
	if result.OutputLines != 0 {
		t.Fatalf("expected zero output lines, got %d", result.OutputLines)
	}
	if !strings.Contains(result.Content, "First line exceeds byte limit") {
		t.Fatalf("expected explanatory content, got %q", result.Content)
	}
}

func TestTruncateTail_LongSingleLineUsesUTF8SafePartial(t *testing.T) {
	input := "prefix-" + strings.Repeat("界", 10)
	result := truncateTail(input, 2000, 8)

	if !result.Truncated {
		t.Fatal("expected truncation")
	}
	if result.TruncatedBy != TruncatedByBytes {
		t.Fatalf("expected byte truncation, got %q", result.TruncatedBy)
	}
	if !result.LastLinePartial {
		t.Fatal("expected partial last line")
	}
	if result.OutputLines != 1 {
		t.Fatalf("expected one output line, got %d", result.OutputLines)
	}
	if !utf8.ValidString(strings.TrimSuffix(strings.TrimSpace(strings.Split(result.Content, "\n")[3]), "\n")) {
		t.Fatal("expected UTF-8 valid partial output")
	}
}

func TestTruncateTextUsesUTF8SafeByteLimit(t *testing.T) {
	got, truncated := TruncateText(strings.Repeat("界", 10), 8)
	if !truncated || got != "界界" || len(got) > 8 || !utf8.ValidString(got) {
		t.Fatalf("TruncateText = %q (%d bytes, valid=%t, truncated=%t), want two valid runes", got, len(got), utf8.ValidString(got), truncated)
	}
}

func TestTruncateStillReturnsTruncatedContentWhenTempSaveFails(t *testing.T) {
	input := strings.Repeat("line\n", 2500)
	t.Setenv("TMPDIR", "/path/that/does/not/exist")

	result := TruncateHead(input)
	if !result.Truncated {
		t.Fatal("expected truncation")
	}
	if strings.Contains(result.Content, input) {
		t.Fatal("expected formatted truncated content, not full original output")
	}
	if !strings.Contains(result.Content, "could not be saved") {
		t.Fatalf("expected fallback footer, got %q", result.Content)
	}
}

func TestMaxLinesEnvVar(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_LINES", "100")
	got := maxLines()
	if got != 100 {
		t.Errorf("expected 100 from env, got %d", got)
	}
}

func TestMaxLinesDefault(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_LINES", "")
	got := maxLines()
	if got != defaultMaxLines {
		t.Errorf("expected default %d, got %d", defaultMaxLines, got)
	}
}

func TestMaxBytesEnvVar(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_BYTES", "1024")
	got := maxBytes()
	if got != 1024 {
		t.Errorf("expected 1024 from env, got %d", got)
	}
}

func TestMaxBytesDefault(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_BYTES", "")
	got := maxBytes()
	if got != defaultMaxBytes {
		t.Errorf("expected default %d, got %d", defaultMaxBytes, got)
	}
}

func TestMaxLinesInvalidEnvVar(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_LINES", "notanumber")
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
