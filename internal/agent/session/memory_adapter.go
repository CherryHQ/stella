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
	rec, err := info.Record()
	if err != nil {
		return err
	}
	ctx = withSessionOwner(ctx, info.UserID, info.GuestID)
	ctx = authz.WithAgentID(ctx, info.AgentID)
	return a.sm.SaveInfo(ctx, rec)
}

func (a *memoryAdapter) rotate(ctx context.Context, expectedSessionID string, successor Info) error {
	rec, err := successor.Record()
	if err != nil {
		return err
	}
	ctx = withSessionOwner(ctx, successor.UserID, successor.GuestID)
	ctx = authz.WithAgentID(ctx, successor.AgentID)
	return a.sm.RotateInfo(ctx, expectedSessionID, rec)
}

func (a *memoryAdapter) load(ctx context.Context, sessionID, userID, agentID string) (Info, error) {
	ctx = withSessionOwner(ctx, userID, authz.GuestIDFromContext(ctx))
	ctx = authz.WithAgentID(ctx, agentID)
	rec, err := a.sm.LoadInfo(ctx, sessionID)
	if err != nil {
		return Info{}, fmt.Errorf("load session %q: %w", sessionID, err)
	}
	info, err := InfoFromRecord(rec)
	if err != nil {
		return Info{}, fmt.Errorf("load session %q: %w", sessionID, err)
	}
	return info, nil
}

func (a *memoryAdapter) list(ctx context.Context, userID, agentID string, opts memory.ListOptions) ([]Info, error) {
	opts.UserID = userID
	opts.AgentID = agentID
	ctx = withSessionOwner(ctx, userID, authz.GuestIDFromContext(ctx))
	ctx = authz.WithAgentID(ctx, agentID)
	recs, err := a.sm.ListInfo(ctx, opts)
	if err != nil {
		return nil, err
	}
	return infosFromRecords(recs)
}

func (a *memoryAdapter) listForAdmin(ctx context.Context, userID, agentID string, opts memory.ListOptions) ([]Info, error) {
	opts.UserID = userID
	opts.AgentID = agentID
	lister, ok := a.sm.(interface {
		ListInfoForAdmin(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error)
	})
	if !ok {
		return nil, fmt.Errorf("memory provider does not support administrative session listing")
	}
	recs, err := lister.ListInfoForAdmin(authz.WithAgentID(ctx, agentID), opts)
	if err != nil {
		return nil, err
	}
	return infosFromRecords(recs)
}

func withSessionOwner(ctx context.Context, userID, guestID string) context.Context {
	if guestID != "" && guestID == userID {
		return authz.WithGuestID(ctx, guestID)
	}
	return authz.WithUserID(ctx, userID)
}

func (a *memoryAdapter) listForReview(ctx context.Context, agentID string, opts memory.ListOptions) ([]Info, error) {
	opts.AgentID = agentID
	if lister, ok := a.sm.(interface {
		ListInfoForReview(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error)
	}); ok {
		recs, err := lister.ListInfoForReview(ctx, opts)
		if err != nil {
			return nil, err
		}
		return infosFromReviewRecords(recs)
	}
	ctx = authz.WithAgentID(ctx, agentID)
	recs, err := a.sm.ListInfo(ctx, opts)
	if err != nil {
		return nil, err
	}
	return infosFromReviewRecords(recs)
}
