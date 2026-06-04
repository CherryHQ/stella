package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// GroupMember represents a bot's membership in a group chat.
type GroupMember struct {
	AgentID        string
	ReplyChannelID string
}

// GroupMemberLister returns the agents that belong to a group.
type GroupMemberLister interface {
	ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error)
}

// handleGroupIncoming processes a group message: append to event log (dedup),
// resolve @mentions, pick the responding agent, and forward to the chat flow.
func (c *Coordinator) handleGroupIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	log := slog.With("component", "group_dispatch", "platform", msg.Platform, "chat_id", msg.ChatID)

	result, err := c.appendGroupMessage(ctx, msg)
	if err != nil {
		return "", false, nil, fmt.Errorf("group event log: %w", err)
	}
	if !result.Inserted {
		log.Debug("group message deduplicated, skipping", "seq", result.Seq)
		return "", false, nil, nil
	}

	log = log.With("group_id", result.GroupID, "seq", result.Seq)
	log.Debug("group message appended")

	c.resolveMentionAgents(ctx, result.GroupID, msg.Platform, msg.Mentions)

	if mentionedAgent := firstMentionedAgent(msg.Mentions); mentionedAgent != "" {
		log.Debug("routing to @mentioned agent", "agent_id", mentionedAgent)
		rc, err := c.resolveGroupChat(ctx, msg, result.GroupID, mentionedAgent)
		if err != nil {
			log.Warn("failed to resolve @mentioned agent, falling back", "agent_id", mentionedAgent, "error", err)
		} else {
			return c.handleResolvedIncoming(ctx, rc, msg, command, args)
		}
	}

	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return "", false, nil, err
	}
	return c.handleResolvedIncoming(ctx, rc, msg, command, args)
}

// appendGroupMessage writes the incoming message to the event log.
func (c *Coordinator) appendGroupMessage(ctx context.Context, msg pkgchannel.IncomingMessage) (eventlog.AppendResult, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	return c.eventLog.AppendGroupMessage(ctx, eventlog.Message{
		Platform:          msg.Platform,
		PlatformGroupID:   msg.ChatID,
		PlatformThreadID:  msg.ThreadID,
		SourceChannelID:   channelID,
		ActorType:         eventlog.ActorHuman,
		ActorID:           msg.SenderID,
		PlatformMessageID: msg.MessageID,
		PlatformTimestamp: msg.Timestamp,
		ReplyTo:           msg.ReplyTo,
		Content:           contentBlocksToText(msg.Content),
	})
}

// resolveMentionAgents fills Mention.AgentID for mentions whose PlatformID
// matches a registered bot that is a member of the group.
func (c *Coordinator) resolveMentionAgents(ctx context.Context, groupID, platform string, mentions []pkgchannel.Mention) {
	if c.botRegistry == nil || c.memberLister == nil || len(mentions) == 0 {
		return
	}

	members, err := c.memberLister.ListGroupMembers(ctx, groupID)
	if err != nil {
		slog.Warn("group dispatch: failed to list group members for mention resolution", "group_id", groupID, "error", err)
		return
	}
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m.AgentID] = struct{}{}
	}

	for i := range mentions {
		if mentions[i].AgentID != "" || mentions[i].PlatformID == "" {
			continue
		}
		channelID, ok := c.botRegistry.ChannelIDForBot(platform, mentions[i].PlatformID)
		if !ok {
			continue
		}
		ch, err := c.store.GetChannel(ctx, channelID)
		if err != nil || ch.AgentID == "" {
			continue
		}
		if _, isMember := memberSet[ch.AgentID]; isMember {
			mentions[i].AgentID = ch.AgentID
		}
	}
}

// resolveGroupChat builds a ResolvedChat for a specific agent in a group,
// bypassing the normal ResolveAgent flow.
func (c *Coordinator) resolveGroupChat(ctx context.Context, msg pkgchannel.IncomingMessage, groupID, agentID string) (*ResolvedChat, error) {
	candidates := orderedIDs(msg.SenderID)
	if len(msg.SenderIDs) > 0 {
		candidates = orderedIDs(append([]string{msg.SenderID}, msg.SenderIDs...)...)
	}
	resolved, match, err := ResolveUserCandidates(ctx, c.auth, msg.Platform, candidates)
	if err != nil {
		return nil, fmt.Errorf("resolve user: %w", err)
	}
	if err := maybeCanonicalizeIdentity(ctx, c.auth, msg.Platform, msg.SenderID, match); err != nil {
		return nil, fmt.Errorf("canonicalize user identity: %w", err)
	}

	svc := c.serviceManager.GetService(agentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found", agentID)
	}

	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	channelCtx := "group:" + msg.ChatID
	if channelID != "" && channelID != msg.Platform {
		channelCtx = "channel:" + channelID + ":" + channelCtx
	}

	return &ResolvedChat{
		Service:    svc,
		User:       resolved.User,
		AgentID:    agentID,
		SessionKey: agent.BuildGroupSessionKey(agentID, groupID),
		Channel:    session.Channel(channelCtx),
		ChatCtx:    ChatContext{Platform: msg.Platform, ChannelID: channelID, ChatID: msg.ChatID, IsGroup: true},
		GroupID:    groupID,
	}, nil
}

// firstMentionedAgent returns the AgentID of the first resolved @mention,
// or "" if none is resolved.
func firstMentionedAgent(mentions []pkgchannel.Mention) string {
	for _, m := range mentions {
		if m.AgentID != "" {
			return m.AgentID
		}
	}
	return ""
}

// contentBlocksToText extracts text from content blocks for event log storage.
func contentBlocksToText(blocks []ai.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if tc, ok := b.(ai.TextContent); ok && tc.Text != "" {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// FuncGroupMemberLister adapts a function to GroupMemberLister.
type FuncGroupMemberLister func(ctx context.Context, groupID string) ([]GroupMember, error)

func (f FuncGroupMemberLister) ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	return f(ctx, groupID)
}
