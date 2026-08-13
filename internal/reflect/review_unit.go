package reflect

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

type ReviewSkipReason string

const (
	reviewSkipOversizedSingleMessage ReviewSkipReason = "oversized_single_message_skipped"
	reviewSkipOversizedBoundaryGroup ReviewSkipReason = "oversized_boundary_group_skipped"
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
	ReviewFromAt    time.Time
	ReviewFromSeq   int64
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

const (
	maxFallbackReviewTokens = 100_000
	maxToolSummaryChars     = 1200
	toolNameSkills          = "skills"

	priorContextOpen       = "<prior_context>\n"
	priorContextClose      = "</prior_context>\n\n"
	freshConversationOpen  = "<fresh_conversation>\n"
	freshConversationClose = "</fresh_conversation>\n"
	skillUsageOpen         = "\n<session_skill_usage>\n"
	skillUsageClose        = "</session_skill_usage>\n"
)

var reviewProtocolReplacer = strings.NewReplacer(
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
	"[user]", "&#91;user&#93;",
	"[assistant]", "&#91;assistant&#93;",
	"[tool]", "&#91;tool&#93;",
	"[system]", "&#91;system&#93;",
)

func (s *Service) buildReviewUnit(ctx context.Context, target reviewTarget, mark reviewWatermark, budget int) (ReviewUnit, error) {
	unit := ReviewUnit{
		ReviewFromAt:    mark.At,
		ReviewFromSeq:   mark.Seq,
		PrivateOneToOne: target.privateOneToOne,
	}
	if !target.privateOneToOne {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNotPrivateOneToOne})
		return unit, nil
	}

	priorLines, fresh, observed, err := s.buildReviewLines(ctx, target, mark)
	if err != nil {
		return ReviewUnit{}, err
	}
	if len(priorLines) == 0 && len(fresh) == 0 && observed.Seq == 0 && observed.At.IsZero() {
		return unit, nil
	}
	if len(fresh) == 0 {
		unit.Skipped = append(unit.Skipped, ReviewSkip{Reason: reviewSkipNoFreshContent})
		if observed.Seq > mark.Seq || observed.At.After(mark.At) {
			unit.LastIncludedSeq = observed.Seq
			unit.LastIncludedAt = mark.At
			if observed.At.After(unit.LastIncludedAt) {
				unit.LastIncludedAt = observed.At
			}
		}
		return unit, nil
	}

	if budget <= 0 {
		budget = maxFallbackReviewTokens
	}

	includedFresh := make([]reviewLine, 0, len(fresh))
	renderedLength := len(freshConversationOpen) + len(freshConversationClose)
	for i := 0; i < len(fresh); {
		group := nextReviewBoundaryGroup(fresh, i)
		i += len(group)

		included, skipped := reviewBoundaryGroupPlan(group, budget)
		unit.Skipped = append(unit.Skipped, skipped...)
		if len(included) == 0 {
			updateReviewUnitWatermark(&unit, group)
			continue
		}

		// Fresh boundaries take priority, so reserve their protocol envelope before
		// considering optional skill usage or prior context.
		candidateLength := renderedLength + renderedReviewLinesLength(included)
		if !reviewTextLengthFitsBudget(candidateLength, budget) {
			if len(includedFresh) == 0 {
				// Timestamp-only history cannot safely split equal-timestamp messages.
				// Skip an impossible boundary as a unit so later reviews can progress.
				unit.Skipped = append(unit.Skipped, oversizedBoundaryGroupSkip(group))
				updateReviewUnitWatermark(&unit, group)
				continue
			}
			unit.Truncated = true
			break
		}

		includedFresh = append(includedFresh, included...)
		renderedLength = candidateLength
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
			renderedUsage := usage.render()
			candidateLength := renderedLength + len(renderedUsage) + 1
			if len(unit.SkillUsage) == 0 {
				candidateLength += len(skillUsageOpen) + len(skillUsageClose)
			}
			if !reviewTextLengthFitsBudget(candidateLength, budget) {
				break
			}
			unit.SkillUsage = append(unit.SkillUsage, usage)
			renderedLength = candidateLength
		}
	}

	includedPriorStart := len(priorLines)
	for i := len(priorLines) - 1; i >= 0; i-- {
		candidateLength := renderedLength + len(priorLines[i]) + 1
		if includedPriorStart == len(priorLines) {
			candidateLength += len(priorContextOpen) + len(priorContextClose)
		}
		if !reviewTextLengthFitsBudget(candidateLength, budget) {
			break
		}
		includedPriorStart = i
		renderedLength = candidateLength
	}

	includedPrior := priorLines[includedPriorStart:]
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
		b.WriteString(priorContextOpen)
		for _, line := range priorLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString(priorContextClose)
	}

	b.WriteString(freshConversationOpen)
	for _, line := range fresh {
		b.WriteString(line.text)
		b.WriteString("\n")
	}
	b.WriteString(freshConversationClose)

	if len(usages) > 0 {
		b.WriteString(skillUsageOpen)
		for _, usage := range usages {
			b.WriteString(usage.render())
			b.WriteString("\n")
		}
		b.WriteString(skillUsageClose)
	}
	return b.String()
}

func reviewTextFitsBudget(text string, budget int) bool {
	return memory.EstimateTokens(text) <= budget
}

func reviewTextLengthFitsBudget(length int, budget int) bool {
	// EstimateTokens is byte-length based. Keep the same calculation here so
	// incremental packing does not repeatedly materialize the whole review unit.
	return (length+3)/4 <= budget
}

func renderedReviewLinesLength(lines []reviewLine) int {
	length := 0
	for _, line := range lines {
		length += len(line.text) + 1
	}
	return length
}

func (s *Service) buildReviewLines(ctx context.Context, target reviewTarget, mark reviewWatermark) ([]string, []reviewLine, reviewWatermark, error) {
	if _, supported := memory.Unwrap(s.memory).(memory.ReviewHistoryReader); !supported {
		return nil, nil, reviewWatermark{}, fmt.Errorf("reflect: Memory provider does not support exact review history")
	}
	rr, ok := s.memory.(memory.ReviewHistoryReader)
	if !ok {
		return nil, nil, reviewWatermark{}, fmt.Errorf("reflect: Memory provider wrapper does not preserve exact review history")
	}
	return s.buildReviewLinesFromReviewHistory(ctx, target, mark, rr)
}

func (s *Service) buildReviewLinesFromReviewHistory(ctx context.Context, target reviewTarget, mark reviewWatermark, rr memory.ReviewHistoryReader) ([]string, []reviewLine, reviewWatermark, error) {
	msgs, err := rr.LoadReviewHistory(ctx, target.session.ID)
	if err != nil {
		return nil, nil, reviewWatermark{}, err
	}
	if len(msgs) == 0 {
		return nil, nil, reviewWatermark{}, nil
	}

	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].FirstSeq < msgs[j].FirstSeq
	})

	var priorLines []string
	var fresh []reviewLine
	var observed reviewWatermark
	skillUsageByCall := map[string]reviewSkillUsage{}
	for _, msg := range msgs {
		lastSeq := msg.LastSeq
		if lastSeq == 0 {
			lastSeq = msg.FirstSeq
		}
		if lastSeq > observed.Seq {
			observed.Seq = lastSeq
		}
		if at := memory.MessageTimestamp(msg.Message); at.After(observed.At) {
			observed.At = at
		}
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
	return priorLines, fresh, observed, nil
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
		// A line is permanently oversized only when it cannot fit in the smallest
		// valid review unit, including the mandatory fresh-conversation envelope.
		minimalLength := len(freshConversationOpen) + len(line.text) + 1 + len(freshConversationClose)
		if !reviewTextLengthFitsBudget(minimalLength, budget) {
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

func oversizedBoundaryGroupSkip(group []reviewLine) ReviewSkip {
	if len(group) == 0 {
		return ReviewSkip{Reason: reviewSkipOversizedBoundaryGroup}
	}
	first := group[0]
	last := group[len(group)-1]
	firstSeq := first.firstSeq
	lastSeq := last.lastSeq
	if lastSeq == 0 {
		lastSeq = last.firstSeq
	}
	return ReviewSkip{
		Reason:   reviewSkipOversizedBoundaryGroup,
		At:       last.at,
		FirstSeq: firstSeq,
		LastSeq:  lastSeq,
		Size:     renderedReviewLinesLength(group),
	}
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
	text, _ = sanitizeSecretLikeContent(text)
	// User-visible text shares a prompt with host protocol markers; neutralize
	// marker lookalikes before composing the bounded review unit.
	return reviewProtocolReplacer.Replace(text)
}
