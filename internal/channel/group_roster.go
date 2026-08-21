package channel

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// NewGroupRosterPromptLoader returns the read-only prompt projection of a
// group's membership. It lives here so the composition root does not reach
// through to SQLC.
func NewGroupRosterPromptLoader(db *pgxpool.Pool) func(context.Context, string, string) prompt.GroupRoster {
	q := sqlc.New(db)
	return func(ctx context.Context, groupID, agentID string) prompt.GroupRoster {
		roster := prompt.GroupRoster{}
		if state, err := q.GetGroupStateByID(ctx, groupID); err == nil {
			roster.Platform = state.Platform
			roster.GroupName = state.GroupName
		}
		members, err := q.ListGroupMembers(ctx, groupID)
		if err != nil {
			return roster
		}
		for _, member := range members {
			name := groupMemberName(ctx, q, member.AgentID)
			if member.AgentID == agentID {
				roster.SelfName = name
				continue
			}
			roster.PeerNames = append(roster.PeerNames, name)
		}
		return roster
	}
}

// groupMemberName resolves a member's display name, falling back to the agent
// id. Peers coordinate by name; ids mean nothing to a model. It goes through
// the participant namer so a roster entry and a transcript line spell the same
// agent the same way.
func groupMemberName(ctx context.Context, q *sqlc.Queries, agentID string) string {
	return eventlog.NewParticipantNamer(q).Name(ctx, "", string(eventlog.ActorAgent), agentID)
}
