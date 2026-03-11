package lcm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBuildPrompt_LeafNormal(t *testing.T) {
	prompt := BuildPrompt("hello world", SummarizeOptions{
		TargetTokens: 100,
	})
	if !strings.Contains(prompt, "Normal summary policy") {
		t.Error("expected normal policy in prompt")
	}
	if !strings.Contains(prompt, "hello world") {
		t.Error("expected text in prompt")
	}
	if !strings.Contains(prompt, "100 tokens") {
		t.Error("expected target tokens in prompt")
	}
}

func TestBuildPrompt_LeafAggressive(t *testing.T) {
	prompt := BuildPrompt("hello", SummarizeOptions{
		Aggressive:   true,
		TargetTokens: 50,
	})
	if !strings.Contains(prompt, "Aggressive summary policy") {
		t.Error("expected aggressive policy in prompt")
	}
}

func TestBuildPrompt_CondensedD1(t *testing.T) {
	prompt := BuildPrompt("summaries here", SummarizeOptions{
		IsCondensed:  true,
		Depth:        1,
		TargetTokens: 200,
	})
	if !strings.Contains(prompt, "leaf-level conversation summaries") {
		t.Error("expected D1 prompt")
	}
}

func TestBuildPrompt_CondensedD2(t *testing.T) {
	prompt := BuildPrompt("summaries here", SummarizeOptions{
		IsCondensed:  true,
		Depth:        2,
		TargetTokens: 200,
	})
	if !strings.Contains(prompt, "session-level summaries") {
		t.Error("expected D2+ prompt")
	}
}

func TestBuildPrompt_DefaultTarget(t *testing.T) {
	// 12 chars → 3 tokens → target = 1
	prompt := BuildPrompt("twelve chars", SummarizeOptions{})
	if !strings.Contains(prompt, "1 tokens") {
		t.Errorf("expected default target in prompt, got:\n%s", prompt)
	}
}

func TestLLMSummarizer_NormalFits(t *testing.T) {
	s := &LLMSummarizer{
		Generate: func(_ context.Context, _ string) (string, error) {
			return "short summary", nil
		},
	}
	result, err := s.Summarize(context.Background(), "long text here", SummarizeOptions{
		TargetTokens: 100,
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if result != "short summary" {
		t.Errorf("result = %q", result)
	}
}

func TestLLMSummarizer_Escalation(t *testing.T) {
	calls := 0
	s := &LLMSummarizer{
		Generate: func(_ context.Context, prompt string) (string, error) {
			calls++
			if calls == 1 {
				// First call returns something too long.
				return strings.Repeat("x", 1000), nil
			}
			// Second call (aggressive) returns short.
			return "condensed", nil
		},
	}
	result, err := s.Summarize(context.Background(), "text", SummarizeOptions{
		TargetTokens: 10,
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (escalation), got %d", calls)
	}
	if result != "condensed" {
		t.Errorf("result = %q", result)
	}
}

func TestLLMSummarizer_DeterministicFallback(t *testing.T) {
	s := &LLMSummarizer{
		Generate: func(_ context.Context, _ string) (string, error) {
			return strings.Repeat("word ", 500), nil // always too long
		},
	}
	result, err := s.Summarize(context.Background(), "text", SummarizeOptions{
		TargetTokens: 10,
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(result, "[Truncated") {
		t.Errorf("expected truncation marker in result: %q", result[:min(100, len(result))])
	}
	// Should be roughly within target.
	if len(result) > 10*4+100 { // target chars + truncation marker overhead
		t.Errorf("result too long: %d chars", len(result))
	}
}

func TestDeterministicFallback(t *testing.T) {
	// Short text — no truncation.
	short := deterministicFallback("hello", 100)
	if short != "hello" {
		t.Errorf("short = %q", short)
	}

	// Long text — truncated at sentence boundary.
	long := "First sentence. Second sentence. Third sentence. Fourth sentence. Fifth sentence."
	result := deterministicFallback(long, 5) // ~20 chars
	if !strings.Contains(result, "[Truncated") {
		t.Error("expected truncation")
	}
}

func TestBuildPrompt_WithPreviousContext(t *testing.T) {
	prompt := BuildPrompt("hello world", SummarizeOptions{
		TargetTokens: 100,
		Previous:     "earlier context about project setup",
	})
	if !strings.Contains(prompt, "earlier context about project setup") {
		t.Error("expected Previous text in prompt")
	}
	if strings.Contains(prompt, "(none)") {
		t.Error("should not contain (none) when Previous is set")
	}
}

func TestLLMSummarizer_FirstGenerateError(t *testing.T) {
	expectedErr := errors.New("llm unavailable")
	s := &LLMSummarizer{
		Generate: func(_ context.Context, _ string) (string, error) {
			return "", expectedErr
		},
	}
	_, err := s.Summarize(context.Background(), "some text", SummarizeOptions{
		TargetTokens: 100,
	})
	if err == nil {
		t.Fatal("expected error from first Generate call")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected wrapped llm error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "summarize:") {
		t.Errorf("expected 'summarize:' prefix, got: %v", err)
	}
}

func TestLLMSummarizer_AggressiveGenerateError(t *testing.T) {
	calls := 0
	expectedErr := errors.New("llm timeout")
	s := &LLMSummarizer{
		Generate: func(_ context.Context, _ string) (string, error) {
			calls++
			if calls == 1 {
				// First call returns something too long to trigger escalation.
				return strings.Repeat("x", 1000), nil
			}
			// Second call (aggressive) fails.
			return "", expectedErr
		},
	}
	_, err := s.Summarize(context.Background(), "text", SummarizeOptions{
		TargetTokens: 10,
	})
	if err == nil {
		t.Fatal("expected error from aggressive Generate call")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected wrapped llm error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "summarize aggressive:") {
		t.Errorf("expected 'summarize aggressive:' prefix, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls before error, got %d", calls)
	}
}

func TestLLMSummarizer_CondensedEscalation(t *testing.T) {
	calls := 0
	s := &LLMSummarizer{
		Generate: func(_ context.Context, prompt string) (string, error) {
			calls++
			if calls == 1 {
				// First call returns too-long output to trigger escalation.
				return strings.Repeat("y", 800), nil
			}
			// Second call (aggressive condensed) returns short.
			return "condensed result", nil
		},
	}
	result, err := s.Summarize(context.Background(), "leaf summary A\nleaf summary B", SummarizeOptions{
		IsCondensed:  true,
		Depth:        1,
		TargetTokens: 10,
	})
	if err != nil {
		t.Fatalf("Summarize condensed: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (condensed escalation), got %d", calls)
	}
	if result != "condensed result" {
		t.Errorf("result = %q", result)
	}
}

func TestDeterministicFallback_LineBreak(t *testing.T) {
	// Build text where the best break point is a newline in the second half.
	// Each line is about 15 chars. targetTokens=5 means ~20 target chars.
	text := "First line here\nSecond line he\nThird line her\nFourth line he"
	result := deterministicFallback(text, 5) // ~20 chars target
	if !strings.Contains(result, "[Truncated") {
		t.Error("expected truncation marker")
	}
	// Should have broken at a newline boundary.
	beforeMarker := strings.Split(result, "\n\n[Truncated")[0]
	if strings.HasSuffix(beforeMarker, " ") {
		t.Error("should not end with trailing space after line break")
	}
}

func TestDeterministicFallback_SentenceBreak(t *testing.T) {
	// Text with no newlines but with a sentence boundary in the second half.
	// targetTokens=15 means ~60 chars target. The ". " at position ~40 is past the midpoint (30).
	text := "AAAAAAAAAAAAAAAAAA. BBBBBBBBBBBBBBBBBBBBB. CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	result := deterministicFallback(text, 15) // ~60 chars target
	if !strings.Contains(result, "[Truncated") {
		t.Error("expected truncation marker")
	}
	// Should have broken at a sentence boundary (". ").
	beforeMarker := strings.Split(result, "\n\n[Truncated")[0]
	if !strings.HasSuffix(beforeMarker, ".") {
		t.Errorf("expected sentence break ending with period, got: %q", beforeMarker)
	}
}

func TestStaticSummarizer(t *testing.T) {
	s := &StaticSummarizer{Response: "fixed"}
	result, err := s.Summarize(context.Background(), "anything", SummarizeOptions{})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if result != "fixed" {
		t.Errorf("result = %q", result)
	}
}
