package access

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ModelUsage is the exact provider-reported accounting for one model within a
// session. Nil totals are deliberate: partial data is not a session total.
type ModelUsage struct {
	Provider          string
	Model             string
	CallCount         int64
	ReportedCallCount int64
	PricedCallCount   int64
	InputTokens       *int64
	OutputTokens      *int64
	CacheReadTokens   *int64
	CacheWriteTokens  *int64
	CostUSD           *float64
}

// Usage is the session-wide rollup plus its per-model breakdown.
type Usage struct {
	PendingCallCount  int64
	CallCount         int64
	ReportedCallCount int64
	PricedCallCount   int64
	InputTokens       *int64
	OutputTokens      *int64
	CacheReadTokens   *int64
	CacheWriteTokens  *int64
	CostUSD           *float64
	Models            []ModelUsage
}

// Usage reads accounting only after the ordinary Session read decision. The
// SQL query is intentionally unscoped because this PEP has already bound the
// session to its durable owner and route agent.
func (a *Access) Usage(ctx context.Context, agentID, sessionID string) (Usage, error) {
	if _, err := a.Read(ctx, agentID, sessionID); err != nil {
		return Usage{}, err
	}
	rows, err := a.svc.q.ListAgentLLMCallUsageBySessionID(ctx, sessionID)
	if err != nil {
		return Usage{}, fmt.Errorf("%w: list session LLM usage: %w", ErrUnavailable, err)
	}

	out := Usage{Models: make([]ModelUsage, 0, len(rows))}
	if a.svc.usage != nil {
		out.PendingCallCount = a.svc.usage.PendingCallCount(sessionID)
	}
	for _, row := range rows {
		item, err := modelUsageFromRow(row)
		if err != nil {
			return Usage{}, err
		}
		out.Models = append(out.Models, item)
		out.CallCount += item.CallCount
		out.ReportedCallCount += item.ReportedCallCount
		out.PricedCallCount += item.PricedCallCount
	}
	if out.CallCount == out.ReportedCallCount {
		out.InputTokens = sumUsage(rows, func(row sqlc.ListAgentLLMCallUsageBySessionIDRow) int64 { return row.InputTokens })
		out.OutputTokens = sumUsage(rows, func(row sqlc.ListAgentLLMCallUsageBySessionIDRow) int64 { return row.OutputTokens })
		out.CacheReadTokens = sumUsage(rows, func(row sqlc.ListAgentLLMCallUsageBySessionIDRow) int64 { return row.CacheReadTokens })
		out.CacheWriteTokens = sumUsage(rows, func(row sqlc.ListAgentLLMCallUsageBySessionIDRow) int64 { return row.CacheWriteTokens })
	}
	if out.CallCount == out.PricedCallCount {
		var total float64
		for _, row := range rows {
			value, err := row.CostUsd.Float64Value()
			if err != nil {
				return Usage{}, fmt.Errorf("%w: decode session LLM cost: %w", ErrUnavailable, err)
			}
			total += value.Float64
		}
		out.CostUSD = &total
	}
	return out, nil
}

func modelUsageFromRow(row sqlc.ListAgentLLMCallUsageBySessionIDRow) (ModelUsage, error) {
	out := ModelUsage{
		Provider: row.Provider, Model: row.Model, CallCount: row.CallCount,
		ReportedCallCount: row.ReportedCallCount, PricedCallCount: row.PricedCallCount,
	}
	if row.CallCount == row.ReportedCallCount {
		out.InputTokens = &row.InputTokens
		out.OutputTokens = &row.OutputTokens
		out.CacheReadTokens = &row.CacheReadTokens
		out.CacheWriteTokens = &row.CacheWriteTokens
	}
	if row.CallCount == row.PricedCallCount {
		value, err := row.CostUsd.Float64Value()
		if err != nil {
			return ModelUsage{}, fmt.Errorf("%w: decode model LLM cost: %w", ErrUnavailable, err)
		}
		out.CostUSD = &value.Float64
	}
	return out, nil
}

func sumUsage(rows []sqlc.ListAgentLLMCallUsageBySessionIDRow, value func(sqlc.ListAgentLLMCallUsageBySessionIDRow) int64) *int64 {
	var total int64
	for _, row := range rows {
		total += value(row)
	}
	return &total
}
