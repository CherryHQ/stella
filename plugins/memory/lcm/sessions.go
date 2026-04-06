package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
)

// SaveInfo implements memory.SessionManager.
func (p *Provider) SaveInfo(ctx context.Context, info memory.SessionInfo) error {
	conv, err := p.q.GetConversationBySessionID(ctx, info.ID)
	if err == sql.ErrNoRows {
		lastActive := info.LastActive
		if lastActive.IsZero() {
			lastActive = time.Now().UTC()
		}
		_, err = p.q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
			SessionID:  info.ID,
			Title:      sql.NullString{String: info.Title, Valid: info.Title != ""},
			Channel:    info.Channel,
			Archived:   boolToInt(info.Archived),
			LastActive: lastActive.UTC().Format("2006-01-02 15:04:05"),
			AgentID:    sql.NullString{String: info.AgentID, Valid: info.AgentID != ""},
			UserID:     sql.NullInt64{Int64: info.UserID, Valid: info.UserID != 0},
		})
		if err != nil {
			return fmt.Errorf("create conversation: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get conversation: %w", err)
	}

	// Update existing conversation fields.
	if info.Title != "" {
		if err := p.q.UpdateConversationTitleBySessionID(ctx, sqlc.UpdateConversationTitleBySessionIDParams{
			Title:     sql.NullString{String: info.Title, Valid: true},
			SessionID: info.ID,
		}); err != nil {
			return fmt.Errorf("update title: %w", err)
		}
	}
	if boolToInt(info.Archived) != conv.Archived {
		if err := p.q.UpdateConversationArchived(ctx, sqlc.UpdateConversationArchivedParams{
			Archived:  boolToInt(info.Archived),
			SessionID: info.ID,
		}); err != nil {
			return fmt.Errorf("update archived: %w", err)
		}
	}
	// Update agent_id/user_id if provided and different.
	if info.AgentID != "" || info.UserID != 0 {
		agentMatch := conv.AgentID.Valid && conv.AgentID.String == info.AgentID
		userMatch := conv.UserID.Valid && conv.UserID.Int64 == info.UserID
		if !agentMatch || !userMatch {
			if err := p.q.UpdateConversationAgentUser(ctx, sqlc.UpdateConversationAgentUserParams{
				AgentID:   sql.NullString{String: info.AgentID, Valid: info.AgentID != ""},
				UserID:    sql.NullInt64{Int64: info.UserID, Valid: info.UserID != 0},
				SessionID: info.ID,
			}); err != nil {
				return fmt.Errorf("update agent/user: %w", err)
			}
		}
	}

	if err := p.q.UpdateConversationLastActive(ctx, info.ID); err != nil {
		return fmt.Errorf("update last_active: %w", err)
	}
	return nil
}

// LoadInfo implements memory.SessionManager.
func (p *Provider) LoadInfo(ctx context.Context, sessionID string) (memory.SessionInfo, error) {
	conv, err := p.q.GetConversationBySessionID(ctx, sessionID)
	if err != nil {
		return memory.SessionInfo{}, fmt.Errorf("get conversation: %w", err)
	}
	return convToSessionInfo(conv), nil
}

// ListInfo implements memory.SessionManager.
func (p *Provider) ListInfo(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	var convs []sqlc.CtxConversation
	var err error
	if opts.IncludeArchived {
		convs, err = p.q.ListConversationsAll(ctx)
	} else {
		convs, err = p.q.ListConversations(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}

	var result []memory.SessionInfo
	for _, c := range convs {
		info := convToSessionInfo(c)
		// Apply filters.
		if opts.AgentID != "" && info.AgentID != opts.AgentID {
			continue
		}
		if opts.UserID != 0 && info.UserID != opts.UserID {
			continue
		}
		result = append(result, info)
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}
	return result, nil
}

// LoadHistory implements memory.SessionManager.
func (p *Provider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	conv, err := p.q.GetConversationBySessionID(ctx, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	return rowsToMessages(msgs), nil
}

func convToSessionInfo(conv sqlc.CtxConversation) memory.SessionInfo {
	info := memory.SessionInfo{
		ID:       conv.SessionID,
		Channel:  conv.Channel,
		Archived: conv.Archived != 0,
	}
	if conv.Title.Valid {
		info.Title = conv.Title.String
	}
	if conv.AgentID.Valid {
		info.AgentID = conv.AgentID.String
	}
	if conv.UserID.Valid {
		info.UserID = conv.UserID.Int64
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.CreatedAt); err == nil {
		info.CreatedAt = t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.LastActive); err == nil {
		info.LastActive = t
	}
	return info
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
