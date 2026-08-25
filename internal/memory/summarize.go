package memory

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CherryHQ/stella/internal/agentrun"
)

// SummarizePolicy constants control how aggressively content is compressed.
const (
	SummarizePolicyNormal     = "normal"
	SummarizePolicyAggressive = "aggressive"
)

// Prompt templates for different summarization modes.

const leafPolicyNormal = `Normal summary policy:
- Preserve key decisions, rationale, constraints, and active tasks.
- Keep essential technical details needed to continue work safely.
- Remove obvious repetition and conversational filler.`

const leafPolicyAggressive = `Aggressive summary policy:
- Keep only durable facts and current task state.
- Remove examples, repetition, and low-value narrative details.
- Preserve explicit TODOs, blockers, decisions, and constraints.`

const leafPromptTemplate = `You summarize a SEGMENT of a conversation for future model turns.
Treat this as incremental memory compaction input, not a full-conversation summary.

%s

Output requirements:
- Plain text only. No preamble, headings, or markdown formatting.
- Track file operations (created, modified, deleted, renamed) with file paths.
- If no file operations appear, include exactly: "Files: none".
- End with: "Expand for details about: <comma-separated list of what was dropped>".
- Target length: about %d tokens or less.

<previous_context>
%s
</previous_context>

<conversation_segment>
%s
</conversation_segment>`

const condensedD1Prompt = `You are compacting leaf-level conversation summaries into a single condensed memory node.
You are preparing context for a fresh model instance that will continue this conversation.

Preserve:
- Decisions made and their rationale when rationale matters going forward.
- Earlier decisions that were superseded, and what replaced them.
- Completed tasks/topics with outcomes.
- In-progress items with current state and what remains.
- Blockers, open questions, and unresolved tensions.

Drop low-value detail:
- Context unchanged from previous_context.
- Intermediate dead ends where the conclusion is already known.
- Tool-internal mechanics and process scaffolding.

Use plain text. Include a timeline with timestamps for significant events.
End with: "Expand for details about: <list>".
Target length: about %d tokens.

<previous_context>
%s
</previous_context>

<summaries>
%s
</summaries>`

const condensedD2Prompt = `You are condensing multiple session-level summaries into a higher-level memory node.
A future model should understand trajectory, not per-session minutiae.

Preserve:
- Decisions still in effect and their rationale.
- Completed work with outcomes.
- Active constraints, limitations, and known issues.
- Current state of in-progress work.

Drop:
- Session-local operational detail.
- Identifiers that are no longer relevant.
- Intermediate states superseded by later outcomes.

Use plain text. Include a timeline with dates for key milestones.
End with: "Expand for details about: <list>".
Target length: about %d tokens.

<previous_context>
%s
</previous_context>

<summaries>
%s
</summaries>`

// Summarizer generates summaries from content.
type Summarizer interface {
	Summarize(ctx context.Context, text string, opts SummarizeOptions) (string, error)
}

// SummarizeOptions controls summarization behavior.
type SummarizeOptions struct {
	IsCondensed  bool
	Depth        int
	Aggressive   bool
	Previous     string // previous summary for continuity
	TargetTokens int
}

// NeutralizeTags prevents user-derived text from opening or closing structural
// prompt tags while keeping the text readable for the model.
func NeutralizeTags(s string, tags ...string) string {
	if s == "" || len(tags) == 0 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '<' || !startsStructuralTag(s[i+1:], tags) {
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(`<\`)
		last = i + 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func startsStructuralTag(s string, tags []string) bool {
	s = strings.TrimPrefix(s, "/")
	for _, tag := range tags {
		if len(s) >= len(tag) && strings.EqualFold(s[:len(tag)], tag) {
			return true
		}
	}
	return false
}

// BuildPrompt constructs the appropriate summarization prompt.
func BuildPrompt(text string, opts SummarizeOptions) string {
	previous := NeutralizeTags(opts.Previous, "conversation_segment", "previous_context")
	if previous == "" {
		previous = "(none)"
	}
	text = NeutralizeTags(text, "conversation_segment", "previous_context")
	target := opts.TargetTokens
	if target <= 0 {
		target = EstimateTokens(text) / 3
	}

	if !opts.IsCondensed {
		policy := leafPolicyNormal
		if opts.Aggressive {
			policy = leafPolicyAggressive
		}
		return fmt.Sprintf(leafPromptTemplate, policy, target, previous, text)
	}

	if opts.Depth <= 1 {
		return fmt.Sprintf(condensedD1Prompt, target, previous, text)
	}
	return fmt.Sprintf(condensedD2Prompt, target, previous, text)
}

// LLMSummarizer generates summaries using an LLM via a callback function.
type LLMSummarizer struct {
	// Generate calls the LLM with the given prompt and returns the response text.
	Generate func(ctx context.Context, prompt string) (string, error)
}

// Summarize generates a summary with escalation: normal -> aggressive -> deterministic fallback.
func (s *LLMSummarizer) Summarize(ctx context.Context, text string, opts SummarizeOptions) (string, error) {
	// First attempt: normal mode.
	prompt := BuildPrompt(text, opts)
	if err := agentrun.Check(ctx); err != nil {
		return "", err
	}
	result, err := s.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}

	target := opts.TargetTokens
	if target <= 0 {
		target = EstimateTokens(text) / 3
	}

	// Check if summary is within budget (allow 50% overshoot before escalating).
	resultTokens := EstimateTokens(result)
	if resultTokens <= target*3/2 {
		return result, nil
	}

	// Escalation 1: aggressive mode.
	opts.Aggressive = true
	prompt = BuildPrompt(text, opts)
	if err := agentrun.Check(ctx); err != nil {
		return "", err
	}
	result, err = s.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("summarize aggressive: %w", err)
	}

	resultTokens = EstimateTokens(result)
	if resultTokens <= target*3/2 {
		return result, nil
	}

	// Escalation 2: deterministic fallback -- truncate to target.
	return deterministicFallback(result, target), nil
}

// deterministicFallback truncates text to approximately targetTokens.
func deterministicFallback(text string, targetTokens int) string {
	targetChars := targetTokens * 4
	if utf8.RuneCountInString(text) <= targetChars {
		return text
	}

	// Truncate at rune boundary near target.
	runes := []rune(text)
	if len(runes) > targetChars {
		runes = runes[:targetChars]
	}

	// Try to break at last sentence or line boundary.
	s := string(runes)
	if idx := strings.LastIndex(s, "\n"); idx > len(s)/2 {
		s = s[:idx]
	} else if idx := strings.LastIndex(s, ". "); idx > len(s)/2 {
		s = s[:idx+1]
	}

	return s + "\n\n[Truncated — expand for full details]"
}

// StaticSummarizer always returns a fixed response (for testing).
type StaticSummarizer struct {
	Response string
	Err      error
}

// Summarize returns the static response.
func (s *StaticSummarizer) Summarize(_ context.Context, _ string, _ SummarizeOptions) (string, error) {
	return s.Response, s.Err
}
