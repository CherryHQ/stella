package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
)

// GroupResolver resolves the canonical group_id for a physical group identity.
type GroupResolver interface {
	ResolveGroupID(ctx context.Context, platform, platformGroupID, platformThreadID string) (string, error)
}

type ResolvedChat struct {
	Service            *agent.Service
	User               auth.User
	AgentID            string
	SessionKey         string
	Channel            session.Channel
	ChatCtx            ChatContext
	GroupID            string          // non-empty for group sessions; used as session scope (D9)
	Authority          authz.Authority // minted from resolved persisted identity/group, never message args
	DedicatedChannelID string          // non-empty only with Authority's exact persisted binding grant
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

// usesMainSession reports whether this chat is pinned to the user's singleton
// main session — a linked user talking on their private channel. Every other
// chat (group, unlinked private channel) is pinned to a key-derived session ID.
func (rc *ResolvedChat) usesMainSession() bool {
	return rc.User.ID != "" && strings.Contains(string(rc.Channel), ":user:")
}

func (rc *ResolvedChat) ResolveSession(ctx context.Context) (session.Info, error) {
	if rc.usesMainSession() {
		return rc.Service.ResolveMainSession(ctx, rc.Authority, rc.User.ID, rc.AgentID)
	}
	if rc.GroupID != "" {
		return rc.Service.ResolveGroupChannelSession(ctx, rc.Authority, rc.SessionKey, rc.GroupID, rc.AgentID, rc.Channel)
	}
	return rc.Service.ResolvePrivateChannelSession(ctx, rc.Authority, rc.SessionKey, rc.sessionUserID(), rc.AgentID, rc.Channel)
}

func (rc *ResolvedChat) CompactSession(ctx context.Context) (string, error) {
	var (
		info session.Info
		err  error
	)
	switch {
	case rc.usesMainSession():
		info, err = rc.Service.ResolveMainSessionForUse(ctx, rc.Authority, rc.User.ID, rc.AgentID)
	case rc.GroupID != "":
		info, err = rc.Service.ResolveGroupChannelSessionForUse(ctx, rc.Authority, rc.SessionKey, rc.GroupID, rc.AgentID, rc.Channel)
	default:
		info, err = rc.Service.ResolvePrivateChannelSessionForUse(ctx, rc.Authority, rc.SessionKey, rc.sessionUserID(), rc.AgentID, rc.Channel)
	}
	if err != nil {
		return "", fmt.Errorf("resolve session for compaction: %w", err)
	}
	return rc.Service.CompactAuthorizedSession(ctx, info)
}

// CurrentSessionForRotation resolves and authorizes the session a `/new` would
// replace. It runs before the rotation is queued so the queued operation carries
// the session the user actually saw; a duplicate `/new` behind it then resolves
// as stale instead of resetting a second time.
func (rc *ResolvedChat) CurrentSessionForRotation(ctx context.Context) (session.Info, error) {
	if !rc.usesMainSession() {
		return session.Info{}, ErrRotationUnsupported
	}
	return rc.Service.ResolveMainSessionForUse(ctx, rc.Authority, rc.User.ID, rc.AgentID)
}

// RotateSession archives the chat's current session and returns its empty
// successor. Only the DM main session rotates today; group and unlinked
// private-channel chats are still pinned to a key-derived session ID.
func (rc *ResolvedChat) RotateSession(ctx context.Context, expectedSessionID string) (session.Info, error) {
	if !rc.usesMainSession() {
		return session.Info{}, ErrRotationUnsupported
	}
	return rc.Service.RotateMainSession(ctx, rc.Authority, rc.User.ID, rc.AgentID, expectedSessionID)
}

// AuthorizeUse performs the fresh execution decision at dequeue time. The
// authority was established from persisted identity/group state while resolving;
// do not reconstruct it from an incoming message here.
func (rc *ResolvedChat) AuthorizeUse(ctx context.Context, access *agentaccess.Service) error {
	if access == nil || !rc.Authority.Valid() {
		return agentaccess.ErrForbidden
	}
	decision, err := access.Begin(ctx, rc.Authority)
	if err != nil {
		return err
	}
	if rc.DedicatedChannelID != "" {
		_, err = decision.UseDedicated(ctx, rc.AgentID, rc.DedicatedChannelID)
	} else {
		_, err = decision.Use(ctx, rc.AgentID)
	}
	return err
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
		Authority:      rc.Authority,
	})
	return stream, info.ID, nil
}

func Resolve(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, accessService *agentaccess.Service, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	return ResolveWithChannel(ctx, sm, store, authStore, accessService, nil, platform, platform, senderID, nil, senderName, chatID, "", isGroup)
}

func ResolveWithChannel(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, accessService *agentaccess.Service, groupResolver GroupResolver, platform, channelID, senderID string, senderIDs []string, senderName, chatID, threadID string, isGroup bool) (*ResolvedChat, error) {
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

	// Resolve the durable canonical group identity before selecting an agent.
	// Agent policy must never be evaluated against an uncanonical platform ID.
	var groupID string
	if isGroup && chatID != "" && groupResolver != nil {
		groupID, err = groupResolver.ResolveGroupID(ctx, platform, chatID, threadID)
		if err != nil {
			return nil, fmt.Errorf("resolve group id: %w", err)
		}
	}
	chatCtx := ChatContext{Platform: platform, ChannelID: channelID, ChatID: chatID, GroupID: groupID, IsGroup: isGroup}

	agentID, err := ResolveAgent(ctx, store, accessService, resolved, chatCtx)
	if err != nil {
		return nil, fmt.Errorf("resolve agent: %w", err)
	}

	role := resolved.User.Role
	if role == "" {
		role = auth.RoleUser
	}
	subject := auth.Subject{UserID: resolved.User.ID, Roles: []string{role}}
	authority, err := subject.Authority()
	if err != nil {
		return nil, ErrAgentAccessDenied
	}
	dedicatedChannelID := ""
	if groupID != "" {
		authority, err = agentaccess.GroupAgentAuthority(groupID, agentID)
		if err != nil {
			return nil, ErrAgentAccessDenied
		}
	} else if channelID != "" {
		// Re-read the persisted channel binding after selection. This is the sole
		// source for a dedicated authority; input routing fields never mint grants.
		if channel, channelErr := store.GetChannel(ctx, channelID); channelErr == nil && channel.AgentID == agentID {
			authority, err = subject.ChannelAuthority(channel.ID)
			if err != nil {
				return nil, ErrAgentAccessDenied
			}
			dedicatedChannelID = channel.ID
		}
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
	switch {
	case groupID != "":
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
		Service:            svc,
		User:               resolved.User,
		AgentID:            agentID,
		SessionKey:         sessionKey,
		Channel:            ch,
		ChatCtx:            chatCtx,
		GroupID:            groupID,
		Authority:          authority,
		DedicatedChannelID: dedicatedChannelID,
	}, nil
}
