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

func TestTurnOutputBudgetSingleCallKeepsPerCallBehavior(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "")
	perCall := TruncateTail(strings.Repeat("x", defaultMaxBytes+1024)).Content
	if len(perCall) <= defaultMaxBytes {
		t.Fatalf("test needs per-call diagnostics above the payload cap, got %d bytes", len(perCall))
	}

	result := ApplyTurnOutputBudget([]string{perCall})[0]
	if result.Truncated || result.Content != perCall {
		t.Fatal("one call must retain the existing per-call truncation result unchanged")
	}
}

func TestTurnOutputBudgetEqualCallsShareEvenly(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "1024")
	outputs := []string{
		strings.Repeat("a", 400),
		strings.Repeat("b", 400),
		strings.Repeat("c", 400),
		strings.Repeat("d", 400),
	}

	results := ApplyTurnOutputBudget(outputs)
	for i, result := range results {
		if !result.Truncated || len(result.Content) != 255 {
			t.Fatalf("result %d = (truncated=%t, bytes=%d), want (true, 255)", i, result.Truncated, len(result.Content))
		}
	}
}

func TestTurnOutputBudgetWaterFillsThreeSmallOneLarge(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "1024")
	outputs := []string{
		strings.Repeat("a", 100),
		strings.Repeat("b", 100),
		strings.Repeat("c", 100),
		strings.Repeat("d", 1000),
	}

	results := ApplyTurnOutputBudget(outputs)
	for i := range 3 {
		if results[i].Truncated || results[i].Content != outputs[i] {
			t.Fatalf("small result %d should surrender only its unused share", i)
		}
	}
	if !results[3].Truncated || len(results[3].Content) > 724 || len(results[3].Content) < 720 {
		t.Fatalf("large result = (truncated=%t, bytes=%d), want a conservative result near its 724-byte share", results[3].Truncated, len(results[3].Content))
	}
}

func TestTurnOutputBudgetExhaustionHasActionableMarker(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "512")
	outputs := []string{strings.Repeat("a", 1000), strings.Repeat("b", 1000)}
	results := ApplyTurnOutputBudget(outputs)

	total := 0
	for i, result := range results {
		total += len(result.Content)
		if !result.Truncated || result.OmittedBytes <= 0 {
			t.Fatalf("result %d did not report omitted bytes: %#v", i, result)
		}
		marker := formatTurnBudgetMarker(result.OmittedBytes)
		if !strings.Contains(result.Content, marker) {
			t.Fatalf("result %d marker is not actionable: %q", i, result.Content)
		}
		kept := len(result.Content) - len(marker)
		if result.OmittedBytes != len(outputs[i])-kept {
			t.Fatalf("result %d omitted %d bytes, want %d", i, result.OmittedBytes, len(outputs[i])-kept)
		}
	}
	if total > 512 {
		t.Fatalf("turn added %d bytes, exceeds hard ceiling 512", total)
	}
}

func TestTurnOutputBudgetEnvironmentOverride(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "600")
	results := ApplyTurnOutputBudget([]string{strings.Repeat("a", 500), strings.Repeat("b", 500)})
	if got := len(results[0].Content) + len(results[1].Content); got > 600 || got < 590 {
		t.Fatalf("environment budget produced %d bytes, want near ceiling 600", got)
	}
}

func TestTurnOutputBudgetUTF8OmittedDigitBoundaryTerminates(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "")
	output := strings.Repeat("🙂a", 5249)
	results := ApplyTurnOutputBudget([]string{output, output, output, output})

	total := 0
	for i, result := range results {
		total += len(result.Content)
		if !result.Truncated {
			t.Fatalf("result %d should be truncated", i)
		}
		if result.OmittedBytes < 9999 || result.OmittedBytes > 10000 {
			t.Fatalf("result %d omitted %d bytes, want decimal-boundary case", i, result.OmittedBytes)
		}
		if !utf8.ValidString(result.Content) {
			t.Fatalf("result %d is not valid UTF-8", i)
		}
	}
	if total > defaultMaxTurnBytes {
		t.Fatalf("turn added %d bytes, exceeds hard ceiling %d", total, defaultMaxTurnBytes)
	}
}

func TestTurnOutputBudgetClampsTinyEnvironmentValue(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "1")
	result := ApplyTurnOutputBudget([]string{"ok"})[0]
	if result.Truncated || result.Content != "ok" {
		t.Fatalf("tiny configured budget should clamp, got %#v", result)
	}
}

func TestTurnOutputBudgetClampsEachShareToActionableMarker(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_TURN_BYTES", "128")
	results := ApplyTurnOutputBudget([]string{strings.Repeat("a", 1000), strings.Repeat("b", 1000)})
	for i, result := range results {
		if !result.Truncated {
			t.Fatalf("result %d should be truncated", i)
		}
		if !strings.Contains(result.Content, "omitted ") || !strings.Contains(result.Content, "Use smaller reads or split the work across turns") {
			t.Fatalf("result %d marker is incomplete: %q", i, result.Content)
		}
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
