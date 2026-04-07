package lcm

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vaayne/anna/pkg/db/sqlc"
)

// getMemoryRow fetches the ctx_agent_memory row, returning nil for non-existent rows.
func (p *Provider) getMemoryRow(ctx context.Context, userID int64, agentID string) (*sqlc.CtxAgentMemory, error) {
	mem, err := p.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mem, nil
}

func (p *Provider) GetProfile(ctx context.Context, userID int64, agentID string) (string, error) {
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	if row == nil {
		return "", nil
	}
	return row.Content, nil
}

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

func (p *Provider) GetAgentSoul(ctx context.Context, userID int64, agentID string) (string, error) {
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("get agent soul: %w", err)
	}
	if row == nil {
		return "", nil
	}
	return row.Soul, nil
}

func (p *Provider) SetAgentSoul(ctx context.Context, userID int64, agentID string, content string) error {
	if err := p.q.UpsertAgentSoul(ctx, sqlc.UpsertAgentSoulParams{
		UserID:  userID,
		AgentID: agentID,
		Soul:    content,
	}); err != nil {
		return fmt.Errorf("set agent soul: %w", err)
	}
	return nil
}
