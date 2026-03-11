package lcm

import (
	"context"
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
