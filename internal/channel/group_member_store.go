package channel

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// NewDBGroupMemberLister returns a GroupMemberLister backed by the group_members
// table, so the composition root passes only the pool.
func NewDBGroupMemberLister(db *pgxpool.Pool) GroupMemberLister {
	return FuncGroupMemberLister(func(ctx context.Context, groupID string) ([]GroupMember, error) {
		rows, err := sqlc.New(db).ListGroupMembers(ctx, groupID)
		if err != nil {
			return nil, err
		}
		members := make([]GroupMember, len(rows))
		for i, r := range rows {
			members[i] = GroupMember{AgentID: r.AgentID, ReplyChannelID: r.ReplyChannelID}
		}
		return members, nil
	})
}
