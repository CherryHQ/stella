package reflect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

const maxFallbackReviewTokens = 100_000

// buildReviewContext formats conversation history for the reviewer.
// Prefers the memory plugin's Reviewer.BuildReviewContext if available.
// Falls back to SessionManager.LoadHistory with truncation.
func (s *Service) buildReviewContext(ctx context.Context, sess memory.Session, since time.Time) (string, error) {
	// Prefer the memory plugin's optimised formatting.
	if rv, ok := s.memory.(memory.Reviewer); ok {
		return rv.BuildReviewContext(ctx, sess, since)
	}

	// Fallback: load raw history via SessionManager.
	sm, ok := s.memory.(memory.SessionManager)
	if !ok {
		return "", nil
	}

	msgs, err := sm.LoadHistory(ctx, sess.ID)
	if err != nil {
		return "", err
	}

	if len(msgs) == 0 {
		return "", nil
	}

	// Split into prior context and fresh content, skipping tool results.
	var prior, fresh []string
	for _, m := range msgs {
		if memory.MessageRole(m) == "tool" {
			continue
		}
		line := fmt.Sprintf("[%s] %s", memory.MessageRole(m), memory.MessageText(m))
		ts := memory.MessageTimestamp(m)
		if !since.IsZero() && !ts.IsZero() && !ts.After(since) {
			prior = append(prior, line)
		} else {
			fresh = append(fresh, line)
		}
	}

	if len(fresh) == 0 {
		return "", nil
	}

	var b strings.Builder

	if len(prior) > 0 {
		b.WriteString("<prior_context>\n")
		for _, line := range tailByBudget(prior, maxFallbackReviewTokens/4) {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("</prior_context>\n\n")
	}

	for _, line := range tailByBudget(fresh, maxFallbackReviewTokens) {
		b.WriteString(line)
		b.WriteString("\n")
	}

	return b.String(), nil
}

// tailByBudget returns the longest suffix of lines that fits within the token budget.
func tailByBudget(lines []string, budget int) []string {
	remaining := budget
	start := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		cost := memory.EstimateTokens(lines[i])
		if remaining-cost < 0 {
			break
		}
		remaining -= cost
		start = i
	}
	return lines[start:]
}
