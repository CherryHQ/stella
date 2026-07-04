package session

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

// memoryAdapter implements store over memory.SessionManager.
type memoryAdapter struct {
	sm memory.SessionManager
}

func newMemoryAdapter(sm memory.SessionManager) *memoryAdapter {
	return &memoryAdapter{sm: sm}
}

func (a *memoryAdapter) save(ctx context.Context, info Info) error {
	ctx = authz.WithUserID(ctx, info.UserID)
	ctx = authz.WithAgentID(ctx, info.AgentID)
	return a.sm.SaveInfo(ctx, info)
}

func (a *memoryAdapter) load(ctx context.Context, sessionID, userID, agentID string) (Info, error) {
	ctx = authz.WithUserID(ctx, userID)
	ctx = authz.WithAgentID(ctx, agentID)
	info, err := a.sm.LoadInfo(ctx, sessionID)
	if err != nil {
		return Info{}, fmt.Errorf("load session %q: %w", sessionID, err)
	}
	return info, nil
}

func (a *memoryAdapter) list(ctx context.Context, userID, agentID string, opts memory.ListOptions) ([]Info, error) {
	opts.UserID = userID
	opts.AgentID = agentID
	ctx = authz.WithUserID(ctx, userID)
	ctx = authz.WithAgentID(ctx, agentID)
	return a.sm.ListInfo(ctx, opts)
}

func (a *memoryAdapter) listForReview(ctx context.Context, agentID string, opts memory.ListOptions) ([]Info, error) {
	opts.AgentID = agentID
	if lister, ok := a.sm.(interface {
		ListInfoForReview(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error)
	}); ok {
		return lister.ListInfoForReview(ctx, opts)
	}
	ctx = authz.WithAgentID(ctx, agentID)
	return a.sm.ListInfo(ctx, opts)
}
