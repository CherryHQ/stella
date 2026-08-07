package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// GroupResolver resolves the canonical group_id for a physical group identity.
type GroupResolver interface {
	ResolveGroupID(ctx context.Context, platform, platformGroupID, platformThreadID string) (string, error)
}

type ResolvedChat struct {
	Service                    *agent.Service
	User                       auth.User
	AgentID                    string
	SessionKey                 string
	Channel                    session.Channel
	ChatCtx                    ChatContext
	GroupID                    string          // non-empty for group sessions; used as session scope (D9)
	GuestID                    string          // durable unlinked channel principal
	GuestMessageLimitPerMinute int             // persisted per-guest admission budget
	Authority                  authz.Authority // minted from resolved persisted identity/group, never message args
	DedicatedChannelID         string          // non-empty only with Authority's exact persisted binding grant
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
	if rc.GuestID != "" {
		return rc.GuestID
	}
	return rc.User.ID
}

// usesMainSession reports whether this chat is pinned to the user's singleton
// main session — a linked user talking on their private channel. Every other
// chat (group, unlinked private channel) is pinned to a key-derived session ID.
func (rc *ResolvedChat) usesMainSession() bool {
	return rc.User.ID != "" && strings.Contains(string(rc.Channel), ":user:")
}

// queueKey is the per-session FIFO boundary for turns, `/new` and `/abort`.
// It must match the session the chat resolves to, not the channel instance the
// message arrived on: a linked user's channels all share one main session
// (ResolveMain ignores the channel context baked into SessionKey), so they
// must share one queue slot too — otherwise a `/new` on one channel skips past
// a turn still running on another and rotates the session underneath it.
// Group and unlinked private chats keep SessionKey, which for them IS the
// session binding.
func (rc *ResolvedChat) queueKey() string {
	if rc.usesMainSession() {
		return rc.AgentID + ":user:" + rc.User.ID
	}
	return rc.SessionKey
}

// chatChannelRequest describes this chat's durable session binding. It is used
// by every chat that is not pinned to the user's singleton main session: group
// chats and private channel chats resolve (and rotate) by binding rather than by
// their derived session key.
func (rc *ResolvedChat) chatChannelRequest() agent.ChatChannelRequest {
	return agent.ChatChannelRequest{
		Authority:  rc.Authority,
		UserID:     rc.sessionUserID(),
		GroupID:    rc.GroupID,
		GuestID:    rc.GuestID,
		AgentID:    rc.AgentID,
		Channel:    rc.Channel,
		SessionKey: rc.SessionKey,
	}
}

// withChatBinding marks a turn's context as backed by this durable chat
// binding. It is the sole entry gate for work that may only run inside a chat
// a user can keep talking in: a Web/API send, a webhook, or a scheduler run
// never passes through here, so it never carries the marker.
//
// A session row cannot stand in for this — the Web UI can open the same main
// session a DM is pinned to — so the adapter that knows has to say so.
func (rc *ResolvedChat) withChatBinding(ctx context.Context) context.Context {
	return agentctx.WithChatBinding(ctx, agentctx.ChatBinding{
		Main:       rc.usesMainSession(),
		Channel:    string(rc.Channel),
		SessionKey: rc.SessionKey,
	})
}

func (rc *ResolvedChat) ResolveSession(ctx context.Context) (session.Info, error) {
	if rc.usesMainSession() {
		return rc.Service.ResolveMainSession(ctx, rc.Authority, rc.User.ID, rc.AgentID)
	}
	return rc.Service.ResolveChatChannelSession(ctx, rc.chatChannelRequest())
}

// resolveSessionForUse resolves the chat's current session and takes the fresh
// execute decision on it, which every mutating command (`/compact`, `/new`) runs
// before touching the session.
func (rc *ResolvedChat) resolveSessionForUse(ctx context.Context) (session.Info, error) {
	if rc.usesMainSession() {
		return rc.Service.ResolveMainSessionForUse(ctx, rc.Authority, rc.User.ID, rc.AgentID)
	}
	return rc.Service.ResolveChatChannelSessionForUse(ctx, rc.chatChannelRequest())
}

func (rc *ResolvedChat) CompactSession(ctx context.Context) (string, error) {
	info, err := rc.resolveSessionForUse(ctx)
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
	return rc.resolveSessionForUse(ctx)
}

// RotateSession archives the chat's current session and returns its empty
// successor. A DM rotates the user's main session; every other chat rotates the
// session its durable channel binding points at.
func (rc *ResolvedChat) RotateSession(ctx context.Context, expectedSessionID string) (session.Info, error) {
	if rc.usesMainSession() {
		return rc.Service.RotateMainSession(ctx, rc.Authority, rc.User.ID, rc.AgentID, expectedSessionID)
	}
	return rc.Service.RotateChatChannelSession(ctx, rc.chatChannelRequest(), expectedSessionID)
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
		_, err = decision.UseDedicatedForType(ctx, rc.AgentID, rc.DedicatedChannelID, rc.ChatCtx.Platform)
	} else {
		_, err = decision.Use(ctx, rc.AgentID)
	}
	return err
}

// AuthorizeDedicatedUse rechecks platform-specific persisted channel state for
// operations that otherwise authorize through the session service. Ordinary
// user and group chats have no dedicated channel grant to recheck.
func (rc *ResolvedChat) AuthorizeDedicatedUse(ctx context.Context, access *agentaccess.Service) error {
	if rc.DedicatedChannelID == "" {
		return nil
	}
	return rc.AuthorizeUse(ctx, access)
}

func (rc *ResolvedChat) Chat(ctx context.Context, message agent.MessageContent) (<-chan agent.Event, string, error) {
	if rc.User.ID == "" && rc.GroupID == "" && rc.GuestID == "" {
		return nil, "", fmt.Errorf("missing user context")
	}
	if rc.AgentID == "" {
		return nil, "", fmt.Errorf("missing agent context")
	}
	info, err := rc.ResolveSession(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("resolve session: %w", err)
	}
	stream := rc.Service.Chat(rc.withChatBinding(ctx), agent.ChatRequest{
		SessionID:      info.ID,
		UserID:         rc.sessionUserID(),
		AgentID:        rc.AgentID,
		Kind:           session.Kind(info.Kind),
		GroupID:        rc.GroupID,
		GuestID:        rc.GuestID,
		Channel:        rc.Channel,
		Message:        message,
		CurrentSpeaker: rc.CurrentSpeaker,
		Authority:      rc.Authority,
	})
	return stream, info.ID, nil
}

func Resolve(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, accessService *agentaccess.Service, platform, senderID, senderName, chatID string, isGroup bool) (*ResolvedChat, error) {
	return ResolveWithChannel(ctx, sm, store, authStore, accessService, nil, nil, platform, platform, senderID, nil, senderName, chatID, "", isGroup)
}

func ResolveWithChannel(ctx context.Context, sm agent.ServiceManager, store config.Store, authStore channelAuthStore, accessService *agentaccess.Service, groupResolver GroupResolver, guests GuestStore, platform, channelID, senderID string, senderIDs []string, senderName, chatID, threadID string, isGroup bool) (*ResolvedChat, error) {
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

	var guestID, agentID string
	guestMessageLimitPerMinute := 0
	if resolved.User.ID == "" && !isGroup {
		channel, channelErr := store.GetChannel(ctx, channelID)
		if channelErr != nil || channel.Type != platform || channel.AgentID == "" || !pkgchannel.AllowsUnlinkedGuestDM(channel.Type, channel.Enabled, channel.Config) || guests == nil {
			return nil, ErrAgentAccessDenied
		}
		guestConfig, configErr := pkgchannel.DecodeGuestConfig(channel.Type, channel.Config)
		if configErr != nil {
			return nil, ErrAgentAccessDenied
		}
		guest, guestErr := guests.ResolveOrCreateGuest(ctx, channel.ID, platform, senderID, guestConfig.GuestMaxPerChannel)
		if guestErr != nil {
			if errors.Is(guestErr, ErrGuestLimitReached) {
				return nil, ErrAgentAccessDenied
			}
			return nil, fmt.Errorf("resolve guest: %w", guestErr)
		}
		guestID, agentID = guest.ID, channel.AgentID
		guestMessageLimitPerMinute = guestConfig.GuestMessageLimitPerMinute
	} else {
		agentID, err = ResolveAgent(ctx, store, accessService, resolved, chatCtx)
		if err != nil {
			return nil, fmt.Errorf("resolve agent: %w", err)
		}
	}

	var authority authz.Authority
	var subject auth.Subject
	if guestID == "" {
		role := resolved.User.Role
		if role == "" {
			role = auth.RoleUser
		}
		subject = auth.Subject{UserID: resolved.User.ID, Roles: []string{role}}
		authority, err = subject.Authority()
		if err != nil {
			return nil, ErrAgentAccessDenied
		}
	}
	dedicatedChannelID := ""
	switch {
	case guestID != "":
		authority, err = authz.NewGuestAuthority(authz.GuestID(guestID), channelID)
		if err != nil {
			return nil, ErrAgentAccessDenied
		}
		dedicatedChannelID = channelID
	case groupID != "":
		authority, err = agentaccess.GroupAgentAuthority(groupID, agentID)
		if err != nil {
			return nil, ErrAgentAccessDenied
		}
	case channelID != "":
		// Re-read the persisted channel binding after selection. This is the sole
		// source for a dedicated authority; input routing fields never mint grants.
		if channel, channelErr := store.GetChannel(ctx, channelID); channelErr == nil && channel.Enabled && channel.Type == platform && channel.AgentID == agentID {
			authority, err = subject.ChannelAuthority(channel.ID)
			if err != nil {
				return nil, ErrAgentAccessDenied
			}
			dedicatedChannelID = channel.ID
		} else if channelID != platform {
			return nil, ErrAgentAccessDenied
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
	case guestID != "":
		sessionKey = agent.BuildSessionKey(agentID, platform, guestID, channelCtx)
	default:
		sessionKey = agent.BuildSessionKey(agentID, platform, senderID, channelCtx)
	}

	ch := session.Channel(sessionKey)
	if groupID != "" {
		ch = session.Channel(channelCtx)
	}

	return &ResolvedChat{
		Service:                    svc,
		User:                       resolved.User,
		AgentID:                    agentID,
		SessionKey:                 sessionKey,
		Channel:                    ch,
		ChatCtx:                    chatCtx,
		GroupID:                    groupID,
		GuestID:                    guestID,
		GuestMessageLimitPerMinute: guestMessageLimitPerMinute,
		Authority:                  authority,
		DedicatedChannelID:         dedicatedChannelID,
	}, nil
}
