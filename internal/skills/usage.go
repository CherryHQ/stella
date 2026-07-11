package skills

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TouchReflectSkillRuntimeUse records a successful runtime load of a
// Reflect-owned user_agent skill. The SQL query rechecks ownership and status.
func (s *PGStore) TouchReflectSkillRuntimeUse(ctx context.Context, skillID string, userID string, agentID string) error {
	if err := s.q.TouchReflectSkillRuntimeUse(ctx, sqlc.TouchReflectSkillRuntimeUseParams{
		SkillID: skillID,
		UserID:  userID,
		AgentID: agentID,
	}); err != nil {
		return fmt.Errorf("skills: touch reflect skill usage: %w", err)
	}
	return nil
}
