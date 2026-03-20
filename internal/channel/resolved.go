package channel

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
// Created once per incoming message via Resolve, then threaded
// through all handler and command paths.
type ResolvedChat struct {
	Pool       *agent.Pool
	User       config.User
	AgentID    string
	SessionKey string
	ChatCtx    ChatContext
	// Auth fields (populated when auth is enabled).
	AuthUserID int64
	Roles      []string
}

// UserID is a convenience accessor for rc.User.ID.
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
// Returns the event channel and session ID.
func (rc *ResolvedChat) Chat(ctx context.Context, message runner.MessageContent, opts ...agent.ChatOption) (<-chan runner.Event, string, error) {
	info, err := rc.ResolveSession()
	if err != nil {
		return nil, "", fmt.Errorf("resolve session: %w", err)
	}
	return rc.Pool.Chat(ctx, info.ID, message, opts...), info.ID, nil
}

// Resolve performs the full user -> agent -> pool -> session key resolution.
// Call once per incoming message, then pass the result to all handlers.
func Resolve(ctx context.Context, pm *agent.PoolManager, store config.Store, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	user, err := ResolveUser(ctx, store, senderID, platform, senderName)
	if err != nil {
		return nil, fmt.Errorf("resolve user: %w", err)
	}

	return resolveWithUser(ctx, pm, store, user, 0, nil, platform, chatID, isGroup)
}

// ResolveWithAuth performs auth-aware user -> agent -> pool -> session key resolution.
// Uses auth_identities for identity resolution with auto-migration fallback.
func ResolveWithAuth(ctx context.Context, pm *agent.PoolManager, store config.Store, authStore auth.AuthStore, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	resolved, err := ResolveUserWithAuth(ctx, store, authStore, senderID, platform, senderName)
	if err != nil {
		return nil, fmt.Errorf("resolve user: %w", err)
	}

	return resolveWithUser(ctx, pm, store, resolved.User, resolved.AuthUserID, resolved.Roles, platform, chatID, isGroup)
}

// resolveWithUser performs agent -> pool -> session key resolution given a resolved user.
func resolveWithUser(ctx context.Context, pm *agent.PoolManager, store config.Store, user config.User, authUserID int64, roles []string, platform, chatID string, isGroup bool) (*ResolvedChat, error) {
	chatCtx := ChatContext{
		Platform: platform,
		ChatID:   chatID,
		IsGroup:  isGroup,
	}

	agentID, err := ResolveAgent(ctx, store, user, chatCtx)
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
	senderID := user.ExternalID
	sessionKey := agent.BuildSessionKey(agentID, platform, senderID, channelCtx)

	return &ResolvedChat{
		Pool:       pool,
		User:       user,
		AgentID:    agentID,
		SessionKey: sessionKey,
		ChatCtx:    chatCtx,
		AuthUserID: authUserID,
		Roles:      roles,
	}, nil
}
