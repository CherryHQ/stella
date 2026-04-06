package lcm

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vaayne/anna/internal/db/sqlc"
)

// GetProfile implements memory.ProfileStore.
func (p *Provider) GetProfile(ctx context.Context, userID int64, agentID string) (string, error) {
	mem, err := p.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	return mem.Content, nil
}

// SetProfile implements memory.ProfileStore.
func (p *Provider) SetProfile(ctx context.Context, userID int64, agentID string, content string) error {
	if err := p.q.UpsertUserAgentMemory(ctx, sqlc.UpsertUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
		Content: content,
	}); err != nil {
		return fmt.Errorf("set profile: %w", err)
	}
	return nil
}
