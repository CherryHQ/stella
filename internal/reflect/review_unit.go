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
	// Seq fields identify the skipped review boundary when the history source
	// can provide stable ordering. They stay zero for timestamp-only fallback.
	FirstSeq int64
	LastSeq  int64
	Size     int
}

// ReviewUnit is the bounded, fresh conversation window consumed by candidate
// generators. It keeps the watermark boundary explicit so each reflect line can
// advance independently after a successful review.
type ReviewUnit struct {
	Text            string
	LastIncludedAt  time.Time
	LastIncludedSeq int64
	// Truncated means more fresh content exists after LastIncluded*, but it did
	// not fit in this bounded window and must be picked up by a later cycle.
	Truncated       bool
	Skipped         []ReviewSkip
	FreshCount      int
	PrivateOneToOne bool
	SkillUsage      []reviewSkillUsage
}

type reviewSkillUsage struct {
	Action string
	Name   string
	Query  string
	CallID string
}

const maxToolSummaryChars = 1200

var (
	tokenLikePattern        = regexp.MustCompile(`(?i)\b(?:ghp_[a-z0-9_]{16,}|github_pat_[a-z0-9_]{16,}|sk-[a-z0-9_-]{16,})\b`)
	assignmentSecretPattern = regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token)\b\s*[:=]\s*["']?[^\s"']{8,}`)
	reviewProtocolReplacer  = strings.NewReplacer(
		"<prior_context>", "&lt;prior_context&gt;",
		"</prior_context>", "&lt;/prior_context&gt;",
		"<fresh_conversation>", "&lt;fresh_conversation&gt;",
		"</fresh_conversation>", "&lt;/fresh_conversation&gt;",
		"<session_skill_usage>", "&lt;session_skill_usage&gt;",
		"</session_skill_usage>", "&lt;/session_skill_usage&gt;",
		"<candidates_json>", "&lt;candidates_json&gt;",
		"</candidates_json>", "&lt;/candidates_json&gt;",
		"[tool_result_summary]", "&#91;tool_result_summary&#93;",
		"[assistant_tool_call]", "&#91;assistant_tool_call&#93;",
	)
)

func (s *Service) buildReviewUnit(ctx context.Context, target reviewTarget, mark reviewWatermark, budget int) (ReviewUnit, error) {
	unit := ReviewUnit{PrivateOneToOne: target.privateOneToOne}
	if !target.privateOneToOne {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNotPrivateOneToOne})
		return unit, nil
	}

	priorLines, fresh, err := s.buildReviewLines(ctx, target, mark)
	if err != nil {
		return ReviewUnit{}, err
	}
	if len(priorLines) == 0 && len(fresh) == 0 {
		return unit, nil
	}
	if len(fresh) == 0 {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNoFreshContent})
		return unit, nil
	}

	if budget <= 0 {
		budget = maxFallbackReviewTokens
	}

	var includedFresh []reviewLine
	for i := 0; i < len(fresh); {
		group := nextReviewBoundaryGroup(fresh, i)
		i += len(group)

		included, skipped := reviewBoundaryGroupPlan(group, budget)
		if len(included) == 0 {
			unit.Skipped = append(unit.Skipped, skipped...)
			updateReviewUnitWatermark(&unit, group)
			continue
		}

		candidateFresh := append(append([]reviewLine(nil), includedFresh...), included...)
		// Fresh boundaries take priority, so reserve their protocol envelope before
		// considering optional skill usage or prior context.
		if !reviewTextFitsBudget(renderReviewUnitText(nil, candidateFresh, nil), budget) {
			unit.Truncated = true
			break
		}

		unit.Skipped = append(unit.Skipped, skipped...)
		includedFresh = candidateFresh
		unit.FreshCount += len(included)
		updateReviewUnitWatermark(&unit, group)
	}

	if unit.FreshCount == 0 && len(unit.Skipped) == 0 {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNoFreshContent})
	}
	if unit.FreshCount == 0 {
		return unit, nil
	}

	for _, line := range includedFresh {
		for _, usage := range line.usages {
			candidateUsage := append(append([]reviewSkillUsage(nil), unit.SkillUsage...), usage)
			if !reviewTextFitsBudget(renderReviewUnitText(nil, includedFresh, candidateUsage), budget) {
				break
			}
			unit.SkillUsage = candidateUsage
		}
	}

	var includedPrior []string
	for i := len(priorLines) - 1; i >= 0; i-- {
		candidatePrior := append([]string{priorLines[i]}, includedPrior...)
		if !reviewTextFitsBudget(renderReviewUnitText(candidatePrior, includedFresh, unit.SkillUsage), budget) {
			break
		}
		includedPrior = candidatePrior
	}

	unit.Text = renderReviewUnitText(includedPrior, includedFresh, unit.SkillUsage)
	if !reviewTextFitsBudget(unit.Text, budget) {
		return ReviewUnit{}, fmt.Errorf("review unit rendered above token budget")
	}
	return unit, nil
}

func updateReviewUnitWatermark(unit *ReviewUnit, group []reviewLine) {
	if len(group) == 0 {
		return
	}
	last := group[len(group)-1]
	if last.lastSeq > 0 {
		unit.LastIncludedSeq = last.lastSeq
	}
	if !last.at.IsZero() {
		unit.LastIncludedAt = last.at
	}
}

func renderReviewUnitText(priorLines []string, fresh []reviewLine, usages []reviewSkillUsage) string {
	var b strings.Builder
	if len(priorLines) > 0 {
		b.WriteString("<prior_context>\n")
		for _, line := range priorLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("</prior_context>\n\n")
	}

	b.WriteString("<fresh_conversation>\n")
	for _, line := range fresh {
		b.WriteString(line.text)
		b.WriteString("\n")
	}
	b.WriteString("</fresh_conversation>\n")

	if len(usages) > 0 {
		b.WriteString("\n<session_skill_usage>\n")
		for _, usage := range usages {
			b.WriteString(usage.render())
			b.WriteString("\n")
		}
		b.WriteString("</session_skill_usage>\n")
	}
	return b.String()
}

func reviewTextFitsBudget(text string, budget int) bool {
	return memory.EstimateTokens(text) <= budget
}

func (s *Service) buildReviewLines(ctx context.Context, target reviewTarget, mark reviewWatermark) ([]string, []reviewLine, error) {
	if rr, ok := s.memory.(memory.ReviewHistoryReader); ok {
		return s.buildReviewLinesFromReviewHistory(ctx, target, mark, rr)
	}
	return s.buildReviewLinesFromLoadHistory(ctx, target, mark)
}

func (s *Service) buildReviewLinesFromReviewHistory(ctx context.Context, target reviewTarget, mark reviewWatermark, rr memory.ReviewHistoryReader) ([]string, []reviewLine, error) {
	msgs, err := rr.LoadReviewHistory(ctx, target.session.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(msgs) == 0 {
		return nil, nil, nil
	}

	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].FirstSeq < msgs[j].FirstSeq
	})

	var priorLines []string
	var fresh []reviewLine
	skillUsageByCall := map[string]reviewSkillUsage{}
	for _, msg := range msgs {
		usages := collectReviewSkillUsage(msg.Message)
		for _, usage := range usages {
			if usage.CallID != "" {
				skillUsageByCall[usage.CallID] = usage
			}
		}
		line := renderReviewLine(msg.Message, skillUsageByCall)
		if line.text == "" {
			continue
		}
		line.id = msg.ID
		line.firstSeq = msg.FirstSeq
		line.lastSeq = msg.LastSeq
		if line.lastSeq == 0 {
			line.lastSeq = line.firstSeq
		}
		line.usages = usages
		if reviewLineBeforeOrAtWatermark(line, mark) {
			priorLines = append(priorLines, line.text)
			continue
		}
		fresh = append(fresh, line)
	}
	return priorLines, fresh, nil
}

func (s *Service) buildReviewLinesFromLoadHistory(ctx context.Context, target reviewTarget, mark reviewWatermark) ([]string, []reviewLine, error) {
	sm, ok := s.memory.(memory.SessionManager)
	if !ok {
		return nil, nil, nil
	}

	msgs, err := sm.LoadHistory(ctx, target.session.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(msgs) == 0 {
		return nil, nil, nil
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
	skillUsageByCall := map[string]reviewSkillUsage{}
	for _, msg := range msgs {
		usages := collectReviewSkillUsage(msg)
		for _, usage := range usages {
			if usage.CallID != "" {
				skillUsageByCall[usage.CallID] = usage
			}
		}
		line := renderReviewLine(msg, skillUsageByCall)
		if line.text == "" {
			continue
		}
		line.usages = usages
		if !mark.At.IsZero() && !line.at.IsZero() && !line.at.After(mark.At) {
			priorLines = append(priorLines, line.text)
			continue
		}
		fresh = append(fresh, line)
	}
	return priorLines, fresh, nil
}

type reviewLine struct {
	id       string
	role     string
	text     string
	at       time.Time
	firstSeq int64
	lastSeq  int64
	usages   []reviewSkillUsage
}

func nextReviewBoundaryGroup(lines []reviewLine, start int) []reviewLine {
	if start >= len(lines) {
		return nil
	}
	first := lines[start]
	end := start + 1
	if first.lastSeq > 0 {
		return lines[start:end]
	}
	if first.at.IsZero() {
		return lines[start:end]
	}
	for end < len(lines) && lines[end].at.Equal(first.at) {
		end++
	}
	return lines[start:end]
}

func reviewLineBeforeOrAtWatermark(line reviewLine, mark reviewWatermark) bool {
	if mark.Seq > 0 && line.lastSeq > 0 {
		return line.lastSeq <= mark.Seq
	}
	if !mark.At.IsZero() && !line.at.IsZero() {
		return !line.at.After(mark.At)
	}
	return false
}

func reviewBoundaryGroupPlan(group []reviewLine, budget int) ([]reviewLine, []ReviewSkip) {
	included := make([]reviewLine, 0, len(group))
	var skipped []ReviewSkip
	for _, line := range group {
		// Only a line that exceeds the whole budget by itself is permanently
		// skipped; envelope or remaining-capacity overflow must be retried later.
		if memory.EstimateTokens(line.text) > budget {
			lastSeq := line.lastSeq
			if lastSeq == 0 {
				lastSeq = line.firstSeq
			}
			skipped = append(skipped, ReviewSkip{
				Reason:   reviewSkipOversizedSingleMessage,
				Role:     line.role,
				At:       line.at,
				FirstSeq: line.firstSeq,
				LastSeq:  lastSeq,
				Size:     len(line.text),
			})
			continue
		}
		included = append(included, line)
	}
	return included, skipped
}

func renderReviewLine(msg ai.Message, skillUsageByCall map[string]reviewSkillUsage) reviewLine {
	role := memory.MessageRole(msg)
	text := memory.MessageText(msg)
	at := memory.MessageTimestamp(msg)
	if text == "" {
		if role == "assistant" {
			return reviewLine{role: role, text: renderAssistantToolCallSummary(msg), at: at}
		}
		return reviewLine{}
	}
	if role == "tool" {
		toolName, callID := toolResultSource(msg)
		safeToolName := redactReviewText(toolName)
		safeCallID := redactReviewText(callID)
		if usage, ok := skillUsageByCall[callID]; ok && toolName == toolNameSkills && usage.Action == "load" {
			return reviewLine{
				role: role,
				text: fmt.Sprintf("[tool_result_summary] tool=%s call_id=%s loaded_skill_content_omitted", safeToolName, safeCallID),
				at:   at,
			}
		}
		return reviewLine{
			role: role,
			// The host prefix stays literal; all tool-provided fields are redacted first.
			text: fmt.Sprintf("[tool_result_summary] tool=%s call_id=%s %s", safeToolName, safeCallID, summarizeToolResult(text)),
			at:   at,
		}
	}
	return reviewLine{
		role: role,
		text: fmt.Sprintf("[%s] %s", role, redactReviewText(text)),
		at:   at,
	}
}

func renderAssistantToolCallSummary(msg ai.Message) string {
	assistant, ok := msg.(ai.AssistantMessage)
	if !ok {
		return ""
	}
	var lines []string
	for _, block := range assistant.Content {
		call, ok := block.(ai.ToolCall)
		if !ok {
			continue
		}
		action, _ := call.Arguments["action"].(string)
		name, _ := call.Arguments["name"].(string)
		query, _ := call.Arguments["query"].(string)
		var parts []string
		parts = append(parts, fmt.Sprintf("[assistant_tool_call] tool=%s call_id=%s", redactReviewText(call.Name), redactReviewText(call.ID)))
		if action != "" {
			parts = append(parts, "action="+redactReviewText(action))
		}
		if name != "" {
			parts = append(parts, "name="+redactReviewText(name))
		}
		if query != "" {
			parts = append(parts, "query="+redactReviewText(query))
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}

func collectReviewSkillUsage(msg ai.Message) []reviewSkillUsage {
	assistant, ok := msg.(ai.AssistantMessage)
	if !ok {
		return nil
	}
	var usages []reviewSkillUsage
	for _, block := range assistant.Content {
		call, ok := block.(ai.ToolCall)
		if !ok || call.Name != toolNameSkills {
			continue
		}
		action, _ := call.Arguments["action"].(string)
		if action != "load" && action != "search_installed" {
			continue
		}
		name, _ := call.Arguments["name"].(string)
		query, _ := call.Arguments["query"].(string)
		usages = append(usages, reviewSkillUsage{
			Action: action,
			Name:   name,
			Query:  query,
			CallID: call.ID,
		})
	}
	return usages
}

func (u reviewSkillUsage) render() string {
	var parts []string
	parts = append(parts, "- action="+u.Action)
	if u.Name != "" {
		parts = append(parts, "skill="+u.Name)
	}
	if u.Query != "" {
		parts = append(parts, "query="+u.Query)
	}
	if u.CallID != "" {
		parts = append(parts, "call_id="+u.CallID)
	}
	return redactReviewText(strings.Join(parts, " "))
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
	text = redactReviewText(text)
	if len(text) <= maxToolSummaryChars {
		return text
	}
	return text[:maxToolSummaryChars] + "... [truncated]"
}

func redactReviewText(text string) string {
	text = tokenLikePattern.ReplaceAllString(text, "[redacted_secret]")
	text = assignmentSecretPattern.ReplaceAllString(text, "[redacted_secret]")
	// User-visible text shares a prompt with host protocol markers; neutralize
	// marker lookalikes before composing the bounded review unit.
	return reviewProtocolReplacer.Replace(text)
}
