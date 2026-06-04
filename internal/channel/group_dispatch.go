package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
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
// resolve @mentions, run arbiter, and dispatch to the selected agent.
func (c *Coordinator) handleGroupIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	log := slog.With("component", "group_dispatch", "platform", msg.Platform, "chat_id", msg.ChatID)

	// CR-007: /config may contain secrets — block it in groups before event log write.
	if strings.EqualFold(command, "/config") {
		return "⚠️ /config is not available in group chats. Please use it in a direct message.", true, nil, nil
	}

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

	ctx = memory.WithGroupSeq(ctx, result.Seq)

	// Query members once and share with mention resolution and arbiter (CR-005).
	var members []GroupMember
	if c.memberLister != nil {
		var memberErr error
		members, memberErr = c.memberLister.ListGroupMembers(ctx, result.GroupID)
		if memberErr != nil {
			log.Warn("failed to list group members", "error", memberErr)
		}
	}

	c.resolveMentionAgentsWithMembers(ctx, result.GroupID, msg.Platform, msg.Mentions, members)

	// Determine the channel's default agent for fallback.
	channelAgentID := c.resolveChannelAgentID(ctx, msg)

	observerChannelID := msg.ChannelID
	if observerChannelID == "" {
		observerChannelID = msg.Platform
	}

	if c.arbiter != nil {
		decision := c.arbiter.Decide(ctx, result.GroupID, msg.Mentions, members, channelAgentID)
		if decision.Debounced {
			log.Debug("arbiter debounced")
			return "", false, nil, nil
		}
		if len(decision.RespondingAgents) > 0 {
			agentID := decision.RespondingAgents[0]
			replyChannelID := findMemberReplyChannel(members, agentID)
			log.Debug("arbiter selected agent", "agent_id", agentID, "reply_channel_id", replyChannelID)
			// CR-009: only dispatch through observer when reply channels match.
			// Cross-adapter publishing is deferred; mismatched agents fall through
			// to channel default so the observer bot doesn't impersonate another.
			if replyChannelID != "" && replyChannelID != observerChannelID {
				log.Warn("selected agent's reply channel differs from observer, falling back to channel default",
					"agent_id", agentID, "reply_channel_id", replyChannelID, "observer_channel_id", observerChannelID)
			} else {
				rc, err := c.resolveGroupChat(ctx, msg, result.GroupID, agentID, replyChannelID)
				if err != nil {
					log.Warn("failed to resolve arbiter-selected agent, falling back", "agent_id", agentID, "error", err)
				} else {
					return c.handleGroupResolved(ctx, rc, msg, command, args)
				}
			}
		}
	} else if mentionedAgent := firstMentionedAgent(msg.Mentions); mentionedAgent != "" {
		replyChannelID := findMemberReplyChannel(members, mentionedAgent)
		log.Debug("routing to @mentioned agent (no arbiter)", "agent_id", mentionedAgent, "reply_channel_id", replyChannelID)
		if replyChannelID != "" && replyChannelID != observerChannelID {
			log.Warn("mentioned agent's reply channel differs from observer, falling back to channel default",
				"agent_id", mentionedAgent, "reply_channel_id", replyChannelID, "observer_channel_id", observerChannelID)
		} else {
			rc, err := c.resolveGroupChat(ctx, msg, result.GroupID, mentionedAgent, replyChannelID)
			if err != nil {
				log.Warn("failed to resolve @mentioned agent, falling back", "agent_id", mentionedAgent, "error", err)
			} else {
				return c.handleGroupResolved(ctx, rc, msg, command, args)
			}
		}
	}

	rc, err := c.resolve(ctx, msg)
	if err != nil {
		return "", false, nil, err
	}
	// Guard: if resolve() picked the same agent that was rejected by the
	// cross-adapter check, don't respond — otherwise the guard is bypassed.
	if rc.AgentID == channelAgentID && channelAgentID != "" {
		replyChannelID := findMemberReplyChannel(members, channelAgentID)
		if replyChannelID != "" && replyChannelID != observerChannelID {
			log.Warn("fallback agent also rejected by cross-adapter guard, not responding",
				"agent_id", channelAgentID, "reply_channel_id", replyChannelID, "observer_channel_id", observerChannelID)
			return "", false, nil, nil
		}
	}
	return c.handleGroupResolved(ctx, rc, msg, command, args)
}

// resolveChannelAgentID returns the channel's default agent for the message,
// used as fallback when no @mention is present.
func (c *Coordinator) resolveChannelAgentID(ctx context.Context, msg pkgchannel.IncomingMessage) string {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	ch, err := c.store.GetChannel(ctx, channelID)
	if err != nil || ch.AgentID == "" {
		return ""
	}
	return ch.AgentID
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

// resolveMentionAgentsWithMembers fills Mention.AgentID for mentions whose
// PlatformID matches a registered bot that is a member of the group.
// members is the pre-fetched group member list (CR-005: avoids duplicate query).
func (c *Coordinator) resolveMentionAgentsWithMembers(ctx context.Context, _ string, platform string, mentions []pkgchannel.Mention, members []GroupMember) {
	if c.botRegistry == nil || len(mentions) == 0 || len(members) == 0 {
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
// replyChannelID is the agent's registered reply channel from group membership;
// when non-empty it overrides msg.ChannelID for session context (CR-009).
func (c *Coordinator) resolveGroupChat(ctx context.Context, msg pkgchannel.IncomingMessage, groupID, agentID, replyChannelID string) (*ResolvedChat, error) {
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

	channelID := replyChannelID
	if channelID == "" {
		channelID = msg.ChannelID
	}
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

// findMemberReplyChannel returns the ReplyChannelID for the given agent, or "".
func findMemberReplyChannel(members []GroupMember, agentID string) string {
	for _, m := range members {
		if m.AgentID == agentID {
			return m.ReplyChannelID
		}
	}
	return ""
}

// handleGroupResolved wraps handleResolvedIncoming with agent response writeback
// to the event log, so other agents can see this agent's responses.
func (c *Coordinator) handleGroupResolved(ctx context.Context, rc *ResolvedChat, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	text, handled, stream, err := c.handleResolvedIncoming(ctx, rc, msg, command, args)
	if stream != nil && c.eventLog != nil && rc.GroupID != "" {
		stream = c.wrapGroupResponseStream(ctx, rc.GroupID, rc.AgentID, stream)
	}
	return text, handled, stream, err
}

// wrapGroupResponseStream intercepts a ChatStream to collect the agent's text
// response and write it back to the event log as actor_type=agent after the
// stream completes.
func (c *Coordinator) wrapGroupResponseStream(ctx context.Context, groupID, agentID string, stream *pkgchannel.ChatStream) *pkgchannel.ChatStream {
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		var textBuf strings.Builder
		for evt := range stream.Events {
			if evt.Text != "" {
				textBuf.WriteString(evt.Text)
			}
			select {
			case out <- evt:
			case <-ctx.Done():
			}
		}
		if textBuf.Len() > 0 {
			// Use a context detached from the request lifecycle so the event log
			// write survives client disconnects (CR-004).
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if _, err := c.eventLog.AppendToGroup(writeCtx, groupID, eventlog.GroupMessage{
				ActorType: eventlog.ActorAgent,
				ActorID:   agentID,
				Content:   textBuf.String(),
			}); err != nil {
				slog.Warn("group dispatch: failed to write agent response to event log",
					"group_id", groupID, "agent_id", agentID, "error", err)
			}
		}
	}()
	return &pkgchannel.ChatStream{
		Events:    out,
		SessionID: stream.SessionID,
	}
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
