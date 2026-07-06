package lcm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// nullAgent builds a pgtype.Text for an agent_id value.
func requireSessionScope(ctx context.Context, userID, agentID string) (string, string, error) {
	if userID == "" {
		userID = authz.UserIDFromContext(ctx)
	}
	if agentID == "" {
		agentID = authz.AgentIDFromContext(ctx)
	}
	if userID == "" {
		return "", "", fmt.Errorf("missing user context")
	}
	if agentID == "" {
		return "", "", fmt.Errorf("missing agent context")
	}
	return userID, agentID, nil
}

func requireMemorySessionScope(ctx context.Context, session memory.Session) (memory.Session, error) {
	userID, agentID, err := requireSessionScope(ctx, session.UserID, session.AgentID)
	if err != nil {
		return memory.Session{}, err
	}
	session.UserID = userID
	session.AgentID = agentID
	return session, nil
}

func conversationScopeParams(session memory.Session) sqlc.GetConversationBySessionIDParams {
	return sqlc.GetConversationBySessionIDParams{
		SessionID: session.ID,
		UserID:    pgtype.Text{String: session.UserID, Valid: true},
		AgentID:   pgnull.Text(session.AgentID),
	}
}

// SaveInfo implements memory.SessionManager.
func (p *Provider) SaveInfo(ctx context.Context, info memory.SessionInfo) error {
	userID, agentIDValue, err := requireSessionScope(ctx, info.UserID, info.AgentID)
	if err != nil {
		return err
	}
	info.UserID = userID
	info.AgentID = agentIDValue

	_, err = p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: info.ID, UserID: pgtype.Text{String: info.UserID, Valid: true}, AgentID: pgnull.Text(info.AgentID)})
	if errors.Is(err, pgx.ErrNoRows) {
		lastActive := info.LastActive
		if lastActive.IsZero() {
			lastActive = time.Now().UTC()
		}
		kind := info.Kind
		if kind == "" {
			kind = "chat"
		}
		_, err = p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
			ID:         uuid.Must(uuid.NewV7()).String(),
			SessionID:  info.ID,
			Title:      pgtype.Text{String: info.Title, Valid: info.Title != ""},
			Channel:    info.Channel,
			Kind:       kind,
			ProjectID:  pgtype.Text{String: info.ProjectID, Valid: info.ProjectID != ""},
			Archived:   info.Archived,
			LastActive: lastActive.UTC(),
			AgentID:    pgnull.Text(info.AgentID),
			UserID:     pgtype.Text{String: info.UserID, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("create conversation: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get conversation: %w", err)
	}

	if err := p.q.UpdateConversationInfoBySessionID(ctx, sqlc.UpdateConversationInfoBySessionIDParams{
		Title:     pgnull.Text(info.Title),
		Archived:  info.Archived,
		Kind:      pgnull.Text(info.Kind),
		ProjectID: pgnull.Text(info.ProjectID),
		SessionID: info.ID,
		UserID:    pgtype.Text{String: info.UserID, Valid: true},
		AgentID:   pgnull.Text(info.AgentID),
	}); err != nil {
		return fmt.Errorf("update conversation info: %w", err)
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
		UserID:    pgtype.Text{String: userID, Valid: true},
		AgentID:   pgnull.Text(agentID),
	})
	if err != nil {
		return memory.SessionInfo{}, fmt.Errorf("get conversation: %w", err)
	}
	return convToSessionInfo(conv), nil
}

// ListInfo implements memory.SessionManager.
func (p *Provider) ListInfo(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	userID, agentIDValue, err := requireSessionScope(ctx, opts.UserID, opts.AgentID)
	if err != nil {
		return nil, err
	}
	convs, err := p.q.ListConversationsFiltered(ctx, sqlc.ListConversationsFilteredParams{
		UserID:          pgtype.Text{String: userID, Valid: true},
		AgentID:         pgnull.Text(agentIDValue),
		IncludeArchived: boolToInt(opts.IncludeArchived),
		ExcludeInternal: opts.ExcludeInternal,
		Kind:            pgnull.Text(opts.Kind),
		ProjectIDIsNull: boolToInt(opts.ProjectIDIsNull),
		ProjectID:       pgnull.Text(opts.ProjectID),
		Offset:          nonNegativeOffset(opts.Offset),
		Limit:           listLimit(opts.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	return convsToSessionInfo(convs), nil
}

// ListInfoForReview lists review candidates across users for one agent.
func (p *Provider) ListInfoForReview(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	if opts.AgentID == "" {
		return nil, fmt.Errorf("missing agent context")
	}
	convs, err := p.q.ListConversationsForReviewFiltered(ctx, sqlc.ListConversationsForReviewFilteredParams{
		AgentID:         pgnull.Text(opts.AgentID),
		Kind:            pgnull.Text(opts.Kind),
		ProjectIDIsNull: boolToInt(opts.ProjectIDIsNull),
		ProjectID:       pgnull.Text(opts.ProjectID),
		Offset:          nonNegativeOffset(opts.Offset),
		Limit:           listLimit(opts.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list review conversations: %w", err)
	}
	return convsToSessionInfo(convs), nil
}

// LoadHistory implements memory.SessionManager.
func (p *Provider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return nil, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{
		SessionID: sessionID,
		UserID:    pgtype.Text{String: userID, Valid: true},
		AgentID:   pgnull.Text(agentID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
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

// LoadReviewHistory implements memory.ReviewHistoryReader.
func (p *Provider) LoadReviewHistory(ctx context.Context, sessionID string) ([]memory.ReviewMessage, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return nil, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{
		SessionID: sessionID,
		UserID:    pgtype.Text{String: userID, Valid: true},
		AgentID:   pgnull.Text(agentID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	return rowsToReviewMessages(msgs), nil
}

func convsToSessionInfo(convs []sqlc.CtxConversation) []memory.SessionInfo {
	result := make([]memory.SessionInfo, 0, len(convs))
	for _, conv := range convs {
		result = append(result, convToSessionInfo(conv))
	}
	return result
}

func listLimit(limit int) int32 {
	if limit <= 0 {
		return -1
	}
	return int32(limit)
}

func nonNegativeOffset(offset int) int32 {
	if offset <= 0 {
		return 0
	}
	return int32(offset)
}

func convToSessionInfo(conv sqlc.CtxConversation) memory.SessionInfo {
	info := memory.SessionInfo{
		ID:       conv.SessionID,
		Channel:  conv.Channel,
		Kind:     conv.Kind,
		Archived: conv.Archived,
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
	info.CreatedAt = conv.CreatedAt.UTC()
	info.LastActive = conv.LastActive.UTC()
	return info
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
