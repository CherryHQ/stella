package reflect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vaayne/anna/pkg/memory"
)

const maxFallbackMessages = 200

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

	// Split into prior context and fresh content.
	// When timestamps are zero (provider doesn't track them), treat messages
	// as fresh to avoid classifying everything as prior and returning empty.
	var prior, fresh []string
	for _, m := range msgs {
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

	// Include truncated prior context.
	if len(prior) > 0 {
		b.WriteString("<prior_context>\n")
		// Keep last N messages as prior context.
		if len(prior) > maxFallbackMessages {
			prior = prior[len(prior)-maxFallbackMessages:]
		}
		for _, line := range prior {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("</prior_context>\n\n")
	}

	// Write fresh messages verbatim.
	if len(fresh) > maxFallbackMessages {
		fresh = fresh[len(fresh)-maxFallbackMessages:]
	}
	for _, line := range fresh {
		b.WriteString(line)
		b.WriteString("\n")
	}

	return b.String(), nil
}
