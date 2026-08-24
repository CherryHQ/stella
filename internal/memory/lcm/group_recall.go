package lcm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

const maxGroupRecallMessages = 200

// SearchGroupRecall implements memory.GroupRecallSource. The SQL predicates are
// the authorization boundary: a row must remain delivered, public text in this
// exact group, and strictly older than the trusted trigger at every read.
func (p *Provider) SearchGroupRecall(ctx context.Context, groupID string, triggerSeq int64, query string, limit int) ([]memory.GroupRecallResult, error) {
	if groupID == "" || triggerSeq <= 0 || limit <= 0 {
		return []memory.GroupRecallResult{}, nil
	}
	match := normalizeQuery(query)
	if match == "" {
		return []memory.GroupRecallResult{}, nil
	}
	rows, err := p.q.SearchDeliveredGroupRecall(ctx, sqlc.SearchDeliveredGroupRecallParams{
		Match: match, GroupID: groupID, TriggerSeq: triggerSeq, LimitCount: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search delivered group history: %w", err)
	}
	out := make([]memory.GroupRecallResult, 0, len(rows))
	for _, row := range rows {
		result := groupRecallResult(row.ID, row.Seq, row.ActorType, row.ActorDisplayName.String, row.Content, row.CreatedAt)
		result.Snippet = searchSnippet(row.Snippet, row.Content)
		result.Score = row.Score
		out = append(out, result)
	}
	return out, nil
}

// ReadGroupRecall implements memory.GroupRecallSource. It expands whole public
// events nearest to the anchor first, splitting the remaining budget between
// sides and giving unused room to the other side before stopping.
func (p *Provider) ReadGroupRecall(ctx context.Context, groupID string, triggerSeq int64, messageID string, tokenCap int) ([]memory.GroupRecallResult, bool, error) {
	if groupID == "" || triggerSeq <= 0 || messageID == "" {
		return nil, false, memory.ErrGroupRecallNotFound
	}
	anchor, err := p.q.GetDeliveredGroupRecallMessage(ctx, sqlc.GetDeliveredGroupRecallMessageParams{
		ID: messageID, GroupID: groupID, TriggerSeq: triggerSeq,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, memory.ErrGroupRecallNotFound
		}
		return nil, false, fmt.Errorf("get delivered group recall anchor: %w", err)
	}
	anchorResult := groupRecallResult(anchor.ID, anchor.Seq, anchor.ActorType, anchor.ActorDisplayName.String, anchor.Content, anchor.CreatedAt)
	anchorTokens := memory.EstimateTokens(anchor.Content)
	if anchorTokens > tokenCap {
		anchorResult.Content, _ = tools.TruncateText(anchorResult.Content, tokenCap*4)
		return []memory.GroupRecallResult{anchorResult}, true, nil
	}

	limit := int32(maxGroupRecallMessages - 1)
	before, err := p.q.ListDeliveredGroupRecallBeforeSeq(ctx, sqlc.ListDeliveredGroupRecallBeforeSeqParams{
		GroupID: groupID, BeforeSeq: anchor.Seq, LimitCount: limit,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list earlier delivered group history: %w", err)
	}
	after, err := p.q.ListDeliveredGroupRecallAfterSeq(ctx, sqlc.ListDeliveredGroupRecallAfterSeqParams{
		GroupID: groupID, AfterSeq: anchor.Seq, TriggerSeq: triggerSeq, LimitCount: limit,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list later delivered group history: %w", err)
	}

	left := groupRecallResultsBefore(before)
	right := groupRecallResultsAfter(after)
	remaining := tokenCap - anchorTokens
	leftBudget := remaining / 2
	rightBudget := remaining - leftBudget
	leftTaken, leftUsed, _ := takeGroupRecall(left, leftBudget, maxGroupRecallMessages-1)
	rightTaken, rightUsed, _ := takeGroupRecall(right, rightBudget, maxGroupRecallMessages-1-len(leftTaken))

	// A short or absent side should not waste the other side's evidence budget.
	if unused := leftBudget - leftUsed; unused > 0 && len(leftTaken) < len(left) {
		more, used, _ := takeGroupRecall(left[len(leftTaken):], unused, maxGroupRecallMessages-1-len(leftTaken)-len(rightTaken))
		leftTaken = append(leftTaken, more...)
		leftUsed += used
	}
	if unused := rightBudget - rightUsed; unused > 0 && len(rightTaken) < len(right) {
		more, used, _ := takeGroupRecall(right[len(rightTaken):], unused, maxGroupRecallMessages-1-len(leftTaken)-len(rightTaken))
		rightTaken = append(rightTaken, more...)
		rightUsed += used
	}
	// Transfer remaining room once in either direction. The first candidate on a
	// side must fit whole, so this never skips a nearer public event for a later one.
	if unused := tokenCap - anchorTokens - leftUsed - rightUsed; unused > 0 {
		if len(leftTaken) < len(left) {
			more, used, _ := takeGroupRecall(left[len(leftTaken):], unused, maxGroupRecallMessages-1-len(leftTaken)-len(rightTaken))
			leftTaken = append(leftTaken, more...)
			unused -= used
		}
		if unused > 0 && len(rightTaken) < len(right) {
			more, _, _ := takeGroupRecall(right[len(rightTaken):], unused, maxGroupRecallMessages-1-len(leftTaken)-len(rightTaken))
			rightTaken = append(rightTaken, more...)
		}
	}

	// Before rows arrive newest-first; reverse the selected contiguous tail so the
	// response itself is chronological around the anchor.
	reverseGroupResults(leftTaken)
	out := make([]memory.GroupRecallResult, 0, len(leftTaken)+1+len(rightTaken))
	out = append(out, leftTaken...)
	out = append(out, anchorResult)
	out = append(out, rightTaken...)
	truncated := len(leftTaken) < len(left) || len(rightTaken) < len(right)
	return out, truncated, nil
}

func groupRecallResult(id string, seq int64, actorType, displayName, content string, occurredAt time.Time) memory.GroupRecallResult {
	return memory.GroupRecallResult{
		ID: id, Seq: seq, ActorType: actorType, ActorDisplayName: displayName, Content: content, OccurredAt: occurredAt.UTC(),
	}
}

func groupRecallResultsBefore(rows []sqlc.ListDeliveredGroupRecallBeforeSeqRow) []memory.GroupRecallResult {
	out := make([]memory.GroupRecallResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupRecallResult(row.ID, row.Seq, row.ActorType, row.ActorDisplayName.String, row.Content, row.CreatedAt))
	}
	return out
}

func groupRecallResultsAfter(rows []sqlc.ListDeliveredGroupRecallAfterSeqRow) []memory.GroupRecallResult {
	out := make([]memory.GroupRecallResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupRecallResult(row.ID, row.Seq, row.ActorType, row.ActorDisplayName.String, row.Content, row.CreatedAt))
	}
	return out
}

func takeGroupRecall(candidates []memory.GroupRecallResult, budget, countCap int) ([]memory.GroupRecallResult, int, bool) {
	if countCap <= 0 {
		return nil, 0, len(candidates) > 0
	}
	out := make([]memory.GroupRecallResult, 0, min(len(candidates), countCap))
	used := 0
	for _, candidate := range candidates {
		if len(out) == countCap {
			return out, used, true
		}
		tokens := memory.EstimateTokens(candidate.Content)
		if used+tokens > budget {
			return out, used, true
		}
		out = append(out, candidate)
		used += tokens
	}
	return out, used, false
}

func reverseGroupResults(items []memory.GroupRecallResult) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}
