package lcm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// nullAgent builds a sql.NullString for an agent_id value.
func nullAgent(agentID string) sql.NullString {
	return sql.NullString{String: agentID, Valid: agentID != ""}
}

func requireSessionScope(ctx context.Context, userID, agentID string) (string, string, error) {
	if userID == "" {
		userID = memory.UserIDFromContext(ctx)
	}
	if agentID == "" {
		agentID = memory.AgentIDFromContext(ctx)
	}
	if userID == "" {
		return "", "", fmt.Errorf("missing user context")
	}
	if agentID == "" {
		return "", "", fmt.Errorf("missing agent context")
	}
	return userID, agentID, nil
}

// SaveInfo implements memory.SessionManager.
func (p *Provider) SaveInfo(ctx context.Context, info memory.SessionInfo) error {
	userID, agentIDValue, err := requireSessionScope(ctx, info.UserID, info.AgentID)
	if err != nil {
		return err
	}
	info.UserID = userID
	info.AgentID = agentIDValue

	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: info.ID, UserID: sql.NullString{String: info.UserID, Valid: true}, AgentID: nullAgent(info.AgentID)})
	if errors.Is(err, sql.ErrNoRows) {
		lastActive := info.LastActive
		if lastActive.IsZero() {
			lastActive = time.Now().UTC()
		}
		kind := info.Kind
		if kind == "" {
			kind = "chat"
		}
		_, err = p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
			ID:         uuid.NewString(),
			SessionID:  info.ID,
			Title:      sql.NullString{String: info.Title, Valid: info.Title != ""},
			Channel:    info.Channel,
			Kind:       kind,
			ProjectID:  sql.NullString{String: info.ProjectID, Valid: info.ProjectID != ""},
			Archived:   boolToInt(info.Archived),
			LastActive: lastActive.UTC().Format("2006-01-02 15:04:05"),
			AgentID:    nullAgent(info.AgentID),
			UserID:     sql.NullString{String: info.UserID, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("create conversation: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get conversation: %w", err)
	}

	agentID := nullAgent(info.AgentID)

	// Update existing conversation fields.
	if info.Title != "" {
		if err := p.q.UpdateConversationTitleBySessionID(ctx, sqlc.UpdateConversationTitleBySessionIDParams{
			Title:     sql.NullString{String: info.Title, Valid: true},
			SessionID: info.ID,
			UserID:    sql.NullString{String: info.UserID, Valid: true},
			AgentID:   agentID,
		}); err != nil {
			return fmt.Errorf("update title: %w", err)
		}
	}
	if boolToInt(info.Archived) != conv.Archived {
		if err := p.q.UpdateConversationArchived(ctx, sqlc.UpdateConversationArchivedParams{
			Archived:  boolToInt(info.Archived),
			SessionID: info.ID,
			UserID:    sql.NullString{String: info.UserID, Valid: true},
			AgentID:   agentID,
		}); err != nil {
			return fmt.Errorf("update archived: %w", err)
		}
	}
	if (info.Kind != "" && info.Kind != conv.Kind) || (info.ProjectID != "" && (!conv.ProjectID.Valid || info.ProjectID != conv.ProjectID.String)) {
		kind := info.Kind
		if kind == "" {
			kind = conv.Kind
		}
		projectID := sql.NullString{String: info.ProjectID, Valid: info.ProjectID != ""}
		if info.ProjectID == "" && conv.ProjectID.Valid {
			projectID = conv.ProjectID
		}
		if err := p.q.UpdateConversationKindProject(ctx, sqlc.UpdateConversationKindProjectParams{
			Kind:      kind,
			ProjectID: projectID,
			SessionID: info.ID,
			UserID:    sql.NullString{String: info.UserID, Valid: true},
			AgentID:   agentID,
		}); err != nil {
			return fmt.Errorf("update kind/project: %w", err)
		}
	}

	if err := p.q.UpdateConversationLastActive(ctx, sqlc.UpdateConversationLastActiveParams{SessionID: info.ID, UserID: sql.NullString{String: info.UserID, Valid: true}, AgentID: agentID}); err != nil {
		return fmt.Errorf("update last_active: %w", err)
	}
	return nil
}

// LoadInfo implements memory.SessionManager.
func (p *Provider) LoadInfo(ctx context.Context, sessionID string) (memory.SessionInfo, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return memory.SessionInfo{}, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{
		SessionID: sessionID,
		UserID:    sql.NullString{String: userID, Valid: true},
		AgentID:   nullAgent(agentID),
	})
	if err != nil {
		return memory.SessionInfo{}, fmt.Errorf("get conversation: %w", err)
	}
	return convToSessionInfo(conv), nil
}

// ListInfo implements memory.SessionManager.
func (p *Provider) ListInfo(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	var convs []sqlc.CtxConversation
	var err error
	userID, agentIDValue, err := requireSessionScope(ctx, opts.UserID, opts.AgentID)
	if err != nil {
		return nil, err
	}
	agentID := sql.NullString{String: agentIDValue, Valid: true}
	if opts.IncludeArchived {
		convs, err = p.q.ListConversationsAll(ctx, sqlc.ListConversationsAllParams{UserID: sql.NullString{String: userID, Valid: true}, AgentID: agentID})
	} else {
		convs, err = p.q.ListConversations(ctx, sqlc.ListConversationsParams{UserID: sql.NullString{String: userID, Valid: true}, AgentID: agentID})
	}
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}

	var result []memory.SessionInfo
	skipped := 0
	for _, c := range convs {
		info := convToSessionInfo(c)
		// Apply filters.
		if opts.AgentID != "" && info.AgentID != opts.AgentID {
			continue
		}
		if opts.UserID != "" && info.UserID != opts.UserID {
			continue
		}
		if opts.Kind != "" && info.Kind != opts.Kind {
			continue
		}
		if opts.ProjectID != "" && info.ProjectID != opts.ProjectID {
			continue
		}
		if skipped < opts.Offset {
			skipped++
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
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return nil, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{
		SessionID: sessionID,
		UserID:    sql.NullString{String: userID, Valid: true},
		AgentID:   nullAgent(agentID),
	})
	if errors.Is(err, sql.ErrNoRows) {
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
		Kind:     conv.Kind,
		Archived: conv.Archived != 0,
	}
	if conv.Title.Valid {
		info.Title = conv.Title.String
	}
	if conv.AgentID.Valid {
		info.AgentID = conv.AgentID.String
	}
	if conv.UserID.Valid {
		info.UserID = conv.UserID.String
	}
	if conv.ProjectID.Valid {
		info.ProjectID = conv.ProjectID.String
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
