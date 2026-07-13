package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
)

// GroupResolver resolves the canonical group_id for a physical group identity.
type GroupResolver interface {
	ResolveGroupID(ctx context.Context, platform, platformGroupID, platformThreadID string) (string, error)
}

type ResolvedChat struct {
	Service    *agent.Service
	User       auth.User
	AgentID    string
	SessionKey string
	Channel    session.Channel
	ChatCtx    ChatContext
	GroupID    string // non-empty for group sessions; used as session scope (D9)
	// CurrentSpeaker is the per-turn group speaker (personalization target only).
	// Canonical source for group personalization; runtime/session scope still
	// flows from GroupID / sessionUserID(), never from User.ID.
	CurrentSpeaker memory.CurrentSpeaker
}

func (rc *ResolvedChat) UserID() string { return rc.User.ID }

// sessionUserID returns the user_id to use for session operations.
// For group sessions this is the group_id (session scope, D9);
// for DM sessions this is the auth user ID.
func (rc *ResolvedChat) sessionUserID() string {
	if rc.GroupID != "" {
		return rc.GroupID
	}
	return rc.User.ID
}

func (rc *ResolvedChat) ResolveSession(ctx context.Context) (session.Info, error) {
	if rc.User.ID != "" && strings.Contains(string(rc.Channel), ":user:") {
		return rc.Service.ResolveMainSession(ctx, rc.User.ID, rc.AgentID)
	}
	if rc.GroupID != "" {
		return rc.Service.ResolveGroupChannelSession(ctx, rc.SessionKey, rc.GroupID, rc.AgentID, rc.Channel)
	}
	return rc.Service.ResolvePrivateChannelSession(ctx, rc.SessionKey, rc.sessionUserID(), rc.AgentID, rc.Channel)
}

func (rc *ResolvedChat) CompactSession(ctx context.Context) (string, error) {
	info, err := rc.ResolveSession(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve session: %w", err)
	}
	return rc.Service.CompactSession(ctx, info)
}

func (rc *ResolvedChat) Chat(ctx context.Context, message agent.MessageContent) (<-chan agent.Event, string, error) {
	if rc.User.ID == "" && rc.GroupID == "" {
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
		SessionID:      info.ID,
		UserID:         rc.sessionUserID(),
		AgentID:        rc.AgentID,
		Kind:           session.Kind(info.Kind),
		GroupID:        rc.GroupID,
		Channel:        rc.Channel,
		Message:        message,
		CurrentSpeaker: rc.CurrentSpeaker,
	})
	return stream, info.ID, nil
}

func Resolve(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, engine *auth.PolicyEngine, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	return ResolveWithChannel(ctx, sm, store, authStore, engine, nil, platform, platform, senderID, nil, senderName, chatID, "", isGroup)
}

func ResolveWithChannel(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, engine *auth.PolicyEngine, groupResolver GroupResolver, platform, channelID, senderID string, senderIDs []string, senderName, chatID, threadID string, isGroup bool) (*ResolvedChat, error) {
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

	var (
		sessionKey string
		groupID    string
	)
	switch {
	case isGroup && chatID != "" && groupResolver != nil:
		groupID, err = groupResolver.ResolveGroupID(ctx, platform, chatID, threadID)
		if err != nil {
			return nil, fmt.Errorf("resolve group id: %w", err)
		}
		sessionKey = agent.BuildGroupSessionKey(agentID, groupID)
	case resolved.User.ID != "" && !isGroup:
		sessionKey = agent.BuildUserSessionKey(agentID, resolved.User.ID, channelCtx)
	default:
		sessionKey = agent.BuildSessionKey(agentID, platform, senderID, channelCtx)
	}

	ch := session.Channel(sessionKey)
	if groupID != "" {
		ch = session.Channel(channelCtx)
	}

	return &ResolvedChat{
		Service:    svc,
		User:       resolved.User,
		AgentID:    agentID,
		SessionKey: sessionKey,
		Channel:    ch,
		ChatCtx:    chatCtx,
		GroupID:    groupID,
	}, nil
}
