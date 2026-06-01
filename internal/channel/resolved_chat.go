package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
)

type ResolvedChat struct {
	Service    *agent.Service
	User       auth.User
	AgentID    string
	SessionKey string
	Channel    session.Channel
	ChatCtx    ChatContext
}

func (rc *ResolvedChat) UserID() string { return rc.User.ID }

func (rc *ResolvedChat) ResolveSession(ctx context.Context) (agent.SessionInfo, error) {
	if rc.User.ID != "" && strings.Contains(string(rc.Channel), ":user:") {
		return rc.Service.ResolveMainSession(ctx, rc.User.ID, rc.AgentID)
	}
	return rc.Service.ResolveChannelSession(ctx, rc.SessionKey, rc.User.ID, rc.AgentID, rc.Channel)
}

func (rc *ResolvedChat) CompactSession(ctx context.Context) (string, error) {
	info, err := rc.ResolveSession(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve session: %w", err)
	}
	return rc.Service.CompactSession(ctx, info)
}

func (rc *ResolvedChat) Chat(ctx context.Context, message agent.MessageContent) (<-chan agent.Event, string, error) {
	if rc.User.ID == "" {
		return nil, "", fmt.Errorf("missing user context")
	}
	if rc.AgentID == "" {
		return nil, "", fmt.Errorf("missing agent context")
	}
	info, err := rc.ResolveSession(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("resolve session: %w", err)
	}
	stream := rc.Service.Chat(ctx, agent.ChatRequest{
		SessionID: info.ID,
		UserID:    rc.User.ID,
		AgentID:   rc.AgentID,
		Channel:   rc.Channel,
		Message:   message,
	})
	return stream, info.ID, nil
}

func Resolve(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, engine *auth.PolicyEngine, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	return ResolveWithChannel(ctx, sm, store, authStore, engine, platform, platform, senderID, nil, senderName, chatID, isGroup)
}

func ResolveWithChannel(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, engine *auth.PolicyEngine, platform, channelID, senderID string, senderIDs []string, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
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

	svc := sm.GetService(agentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found", agentID)
	}

	channelCtx := "private"
	if isGroup && chatID != "" {
		channelCtx = "group:" + chatID
	}
	if channelID != "" && channelID != platform {
		channelCtx = "channel:" + channelID + ":" + channelCtx
	}

	var sessionKey string
	if resolved.User.ID != "" && !isGroup {
		sessionKey = agent.BuildUserSessionKey(agentID, resolved.User.ID, channelCtx)
	} else {
		sessionKey = agent.BuildSessionKey(agentID, platform, senderID, channelCtx)
	}

	return &ResolvedChat{
		Service:    svc,
		User:       resolved.User,
		AgentID:    agentID,
		SessionKey: sessionKey,
		Channel:    session.Channel(sessionKey),
		ChatCtx:    chatCtx,
	}, nil
}
