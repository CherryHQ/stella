package reflect

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

type ReviewSkipReason string

const (
	reviewSkipOversizedSingleMessage ReviewSkipReason = "oversized_single_message_skipped"
	reviewSkipNoFreshContent         ReviewSkipReason = "no_fresh_content"
	reviewSkipNotPrivateOneToOne     ReviewSkipReason = "not_private_one_to_one"
)

type ReviewSkip struct {
	Reason ReviewSkipReason
	Role   string
	At     time.Time
	Size   int
}

// ReviewUnit is the bounded, fresh conversation window consumed by candidate
// generators. It keeps the watermark boundary explicit so each reflect line can
// advance independently after a successful review.
type ReviewUnit struct {
	Text            string
	LastIncludedAt  time.Time
	Skipped         []ReviewSkip
	FreshCount      int
	PrivateOneToOne bool
}

const maxToolSummaryChars = 1200

var tokenLikePattern = regexp.MustCompile(`(?i)\b(?:ghp_[a-z0-9_]{16,}|github_pat_[a-z0-9_]{16,}|sk-[a-z0-9_-]{16,})\b`)

func (s *Service) buildReviewUnit(ctx context.Context, target reviewTarget, since time.Time, budget int) (ReviewUnit, error) {
	unit := ReviewUnit{PrivateOneToOne: target.privateOneToOne}
	if !target.privateOneToOne {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNotPrivateOneToOne})
		return unit, nil
	}

	sm, ok := s.memory.(memory.SessionManager)
	if !ok {
		return unit, nil
	}

	msgs, err := sm.LoadHistory(ctx, target.session.ID)
	if err != nil {
		return ReviewUnit{}, err
	}
	if len(msgs) == 0 {
		return unit, nil
	}

	sort.SliceStable(msgs, func(i, j int) bool {
		ti, tj := memory.MessageTimestamp(msgs[i]), memory.MessageTimestamp(msgs[j])
		if ti.IsZero() || tj.IsZero() {
			return i < j
		}
		return ti.Before(tj)
	})

	var priorLines []string
	var fresh []reviewLine
	for _, msg := range msgs {
		line := renderReviewLine(msg)
		if line.text == "" {
			continue
		}
		if !since.IsZero() && !line.at.IsZero() && !line.at.After(since) {
			priorLines = append(priorLines, line.text)
			continue
		}
		fresh = append(fresh, line)
	}

	if len(fresh) == 0 {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNoFreshContent})
		return unit, nil
	}

	if budget <= 0 {
		budget = maxFallbackReviewTokens
	}

	var b strings.Builder
	if len(priorLines) > 0 {
		b.WriteString("<prior_context>\n")
		for _, line := range tailByBudget(priorLines, budget/4) {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("</prior_context>\n\n")
	}

	b.WriteString("<fresh_conversation>\n")
	remaining := budget
	for _, line := range fresh {
		cost := memory.EstimateTokens(line.text)
		if cost > budget {
			unit.Skipped = append(unit.Skipped, ReviewSkip{
				Reason: reviewSkipOversizedSingleMessage,
				Role:   line.role,
				At:     line.at,
				Size:   len(line.text),
			})
			if !line.at.IsZero() {
				unit.LastIncludedAt = line.at
			}
			continue
		}
		if remaining-cost < 0 {
			break
		}
		b.WriteString(line.text)
		b.WriteString("\n")
		remaining -= cost
		unit.FreshCount++
		if !line.at.IsZero() {
			unit.LastIncludedAt = line.at
		}
	}
	b.WriteString("</fresh_conversation>\n")

	if unit.FreshCount == 0 && len(unit.Skipped) == 0 {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNoFreshContent})
	}
	if unit.FreshCount == 0 {
		return unit, nil
	}
	unit.Text = b.String()
	return unit, nil
}

type reviewLine struct {
	role string
	text string
	at   time.Time
}

func renderReviewLine(msg ai.Message) reviewLine {
	role := memory.MessageRole(msg)
	text := memory.MessageText(msg)
	at := memory.MessageTimestamp(msg)
	if text == "" {
		return reviewLine{}
	}
	if role == "tool" {
		toolName, callID := toolResultSource(msg)
		return reviewLine{
			role: role,
			text: fmt.Sprintf("[tool_result_summary] tool=%s call_id=%s %s", toolName, callID, summarizeToolResult(text)),
			at:   at,
		}
	}
	return reviewLine{
		role: role,
		text: fmt.Sprintf("[%s] %s", role, text),
		at:   at,
	}
}

func toolResultSource(msg ai.Message) (string, string) {
	tr, ok := msg.(ai.ToolResultMessage)
	if !ok {
		return "unknown", ""
	}
	if tr.ToolName == "" {
		tr.ToolName = "unknown"
	}
	return tr.ToolName, tr.ToolCallID
}

func summarizeToolResult(text string) string {
	text = tokenLikePattern.ReplaceAllString(text, "[REDACTED_TOKEN]")
	if len(text) <= maxToolSummaryChars {
		return text
	}
	return text[:maxToolSummaryChars] + "... [truncated]"
}
