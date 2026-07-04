package main

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func toolOverrideFetcher(q *sqlc.Queries) agent.ToolOverrideFetcher {
	return func(ctx context.Context, userID, agentID string) ([]agent.ToolOverride, error) {
		rows, err := q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
			UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID),
		})
		if err != nil {
			return nil, err
		}
		out := make([]agent.ToolOverride, 0, len(rows))
		for _, row := range rows {
			out = append(out, agent.ToolOverride{ToolName: row.ToolName, Scope: row.Scope, Enabled: row.Enabled})
		}
		return out, nil
	}
}
