package chatroute

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
)

// ResolvedChat holds the fully resolved state for a chat message:
// the target pool, authenticated user, agent, and session key.
type ResolvedChat struct {
	Pool       *agent.Pool
	User       auth.AuthUser
	AgentID    string
	SessionKey string
	ChatCtx    ChatContext
}

// UserID returns the user's ID (0 for unlinked users).
func (rc *ResolvedChat) UserID() int64 { return rc.User.ID }

// ResolveSession returns the active session for this chat, creating one if needed.
func (rc *ResolvedChat) ResolveSession() (agent.SessionInfo, error) {
	return rc.Pool.ResolveSession(rc.SessionKey, rc.User.ID)
}

// RotateSession archives the current session and creates a new one.
func (rc *ResolvedChat) RotateSession() (agent.SessionInfo, error) {
	return rc.Pool.RotateSession(rc.SessionKey, rc.User.ID)
}

// CompactSession compacts the active session for this chat.
func (rc *ResolvedChat) CompactSession(ctx context.Context) (string, error) {
	return rc.Pool.CompactSession(ctx, rc.SessionKey)
}

// Chat resolves the session and streams an agent response.
func (rc *ResolvedChat) Chat(ctx context.Context, message runner.MessageContent, opts ...agent.ChatOption) (<-chan runner.Event, string, error) {
	info, err := rc.ResolveSession()
	if err != nil {
		return nil, "", fmt.Errorf("resolve session: %w", err)
	}
	return rc.Pool.Chat(ctx, info.ID, message, opts...), info.ID, nil
}

// Resolve performs the full user -> agent -> pool -> session key resolution.
func Resolve(ctx context.Context, pm *agent.PoolManager, store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	resolved, err := ResolveUser(ctx, authStore, platform, senderID)
	if err != nil {
		return nil, fmt.Errorf("resolve user: %w", err)
	}

	chatCtx := ChatContext{
		Platform: platform,
		ChatID:   chatID,
		IsGroup:  isGroup,
	}

	agentID, err := ResolveAgent(ctx, store, authStore, engine, resolved, chatCtx)
	if err != nil {
		return nil, fmt.Errorf("resolve agent: %w", err)
	}

	pool := pm.Get(agentID)
	if pool == nil {
		return nil, fmt.Errorf("agent pool %q not found", agentID)
	}

	channelCtx := "private"
	if isGroup && chatID != "" {
		channelCtx = "group:" + chatID
	}

	var sessionKey string
	if resolved.User.ID != 0 && !isGroup {
		sessionKey = agent.BuildUserSessionKey(agentID, resolved.User.ID, channelCtx)
	} else {
		sessionKey = agent.BuildSessionKey(agentID, platform, senderID, channelCtx)
	}

	return &ResolvedChat{
		Pool:       pool,
		User:       resolved.User,
		AgentID:    agentID,
		SessionKey: sessionKey,
		ChatCtx:    chatCtx,
	}, nil
}
