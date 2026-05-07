package channel

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
)

type ResolvedChat struct {
	Pool       *agent.Pool
	User       auth.AuthUser
	AgentID    string
	SessionKey string
	ChatCtx    ChatContext
	IsGroup    bool
}

func (rc *ResolvedChat) UserID() int64 { return rc.User.ID }

func (rc *ResolvedChat) ResolveSession() (agent.SessionInfo, error) {
	return rc.Pool.ResolveSession(rc.SessionKey, rc.User.ID)
}

func (rc *ResolvedChat) RotateSession() (agent.SessionInfo, error) {
	return rc.Pool.RotateSession(rc.SessionKey, rc.User.ID)
}

func (rc *ResolvedChat) CompactSession(ctx context.Context) (string, error) {
	info, err := rc.ResolveSession()
	if err != nil {
		return "", fmt.Errorf("resolve session: %w", err)
	}
	return rc.Pool.CompactSession(ctx, info.ID)
}

func (rc *ResolvedChat) Chat(ctx context.Context, message agent.MessageContent, opts ...agent.ChatOption) (<-chan agent.Event, string, error) {
	info, err := rc.ResolveSession()
	if err != nil {
		return nil, "", fmt.Errorf("resolve session: %w", err)
	}
	return rc.Pool.Chat(ctx, info.ID, message, opts...), info.ID, nil
}

func Resolve(ctx context.Context, pm *agent.PoolManager, store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	return ResolveWithChannel(ctx, pm, store, authStore, engine, platform, platform, senderID, nil, senderName, chatID, isGroup)
}

func ResolveWithChannel(ctx context.Context, pm *agent.PoolManager, store config.Store, authStore auth.AuthStore, engine *auth.PolicyEngine, platform, channelID, senderID string, senderIDs []string, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	if channelID == "" {
		channelID = platform
	}
	candidates := orderedIDs(senderID)
	if len(senderIDs) > 0 {
		candidates = orderedIDs(append([]string{senderID}, senderIDs...)...)
	}
	resolved, match, err := ResolveUserCandidates(ctx, authStore, platform, candidates)
	if err != nil {
		return nil, fmt.Errorf("resolve user: %w", err)
	}
	if err := maybeCanonicalizeIdentity(ctx, authStore, platform, senderID, match); err != nil {
		return nil, fmt.Errorf("canonicalize user identity: %w", err)
	}

	chatCtx := ChatContext{Platform: platform, ChannelID: channelID, ChatID: chatID, IsGroup: isGroup}

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
	if channelID != "" && channelID != platform {
		channelCtx = "channel:" + channelID + ":" + channelCtx
	}

	var sessionKey string
	switch {
	case isGroup && chatID != "":
		// All group members share one session keyed by chat, not by sender.
		sessionKey = agent.BuildGroupSessionKey(agentID, channelID, chatID)
	case resolved.User.ID != 0:
		sessionKey = agent.BuildUserSessionKey(agentID, resolved.User.ID, channelCtx)
	default:
		sessionKey = agent.BuildSessionKey(agentID, platform, senderID, channelCtx)
	}

	return &ResolvedChat{
		Pool:       pool,
		User:       resolved.User,
		AgentID:    agentID,
		SessionKey: sessionKey,
		ChatCtx:    chatCtx,
		IsGroup:    isGroup,
	}, nil
}
