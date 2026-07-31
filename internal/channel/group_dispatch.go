package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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

// handleGroupIncoming ingests a group message and wakes the durable dispatcher.
func (c *Coordinator) handleGroupIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	log := slog.With("component", "group_dispatch", "platform", msg.Platform, "chat_id", msg.ChatID)

	// CR-007: /config may contain secrets — block it in groups before event log write.
	if strings.EqualFold(command, "/config") {
		return "⚠️ /config is not available in group chats. Please use it in a direct message.", true, nil, nil
	}
	// A group's context is shared by every member, so `/new` cannot reset it.
	// Answer before the event-log append so the refused command does not become
	// group context either — it is an instruction to Stella, not something the
	// group said.
	if strings.EqualFold(command, "/new") {
		return pkgchannel.GroupNewSessionUnsupportedMessage, true, nil, nil
	}

	result, err := c.appendGroupMessage(ctx, msg)
	if err != nil {
		return "", false, nil, fmt.Errorf("group event log: %w", err)
	}
	if !result.Inserted {
		log.Debug("group message deduplicated, skipping", "seq", result.Seq)
		return "", false, nil, nil
	}

	log.With("group_id", result.GroupID, "seq", result.Seq).Debug("group message appended")
	if c.groupDispatcher != nil {
		c.groupDispatcher.Wake()
	}
	return "", false, nil, nil
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
		ContentBlocks:     marshalGroupContentBlocks(msg.Content),
	}, eventlog.WithOnInserted(func(ctx context.Context, q *sqlc.Queries, result eventlog.AppendResult) error {
		members, err := q.ListGroupMembers(ctx, result.GroupID)
		if err != nil {
			return fmt.Errorf("list group members: %w", err)
		}
		groupMembers := make([]GroupMember, len(members))
		for i, m := range members {
			groupMembers[i] = GroupMember{AgentID: m.AgentID, ReplyChannelID: m.ReplyChannelID}
		}
		c.resolveMentionAgentsWithMembers(ctx, result.GroupID, msg.Platform, msg.Mentions, groupMembers)
		envelope, err := EncodeGroupOutboxEnvelope(msg.Mentions)
		if err != nil {
			return fmt.Errorf("encode outbox envelope: %w", err)
		}
		_, err = q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
			ID:             uuid.Must(uuid.NewV7()).String(),
			GroupMessageID: result.Message.ID,
			GroupID:        result.GroupID,
			Envelope:       envelope,
			Status:         "pending",
			AttemptCount:   0,
			LeaseUntil:     pgtype.Timestamptz{},
			NextAttemptAt:  pgtype.Timestamptz{},
			LastError:      "",
		})
		if err != nil {
			return fmt.Errorf("create group outbox: %w", err)
		}
		return nil
	}))
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

	log := slog.With("component", "group_dispatch", "platform", platform)
	for i := range mentions {
		if mentions[i].AgentID != "" || mentions[i].PlatformID == "" {
			continue
		}
		channelID, ok := c.botRegistry.ChannelIDForBot(platform, mentions[i].PlatformID)
		if !ok {
			// Registry miss: the mentioned platform id is not a Stella bot, or the
			// owning channel never registered its identity (e.g. the bot open_id
			// fetch failed at startup). Left unresolved; decideResponders will fall
			// back to semantic routing instead of suppressing the turn (#619).
			log.Debug("mention platform id not in bot registry",
				"mention_raw", mentions[i].Raw, "platform_id", mentions[i].PlatformID)
			continue
		}
		ch, err := c.store.GetChannel(ctx, channelID)
		if err != nil || ch.AgentID == "" {
			log.Debug("mention channel lookup missed",
				"mention_raw", mentions[i].Raw, "platform_id", mentions[i].PlatformID,
				"channel_id", channelID, "channel_agent", ch.AgentID, "error", err)
			continue
		}
		if _, isMember := memberSet[ch.AgentID]; !isMember {
			log.Debug("mention resolved to a non-member agent",
				"mention_raw", mentions[i].Raw, "platform_id", mentions[i].PlatformID,
				"channel_agent", ch.AgentID)
			continue
		}
		mentions[i].AgentID = ch.AgentID
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

	// Membership selects a candidate, not an authority. Every group turn gets a
	// fresh roleless GroupAgentActor bound to this exact group/member.
	if c.agentAccess == nil {
		return nil, ErrAgentAccessDenied
	}
	authority, err := agentaccess.GroupAgentAuthority(groupID, agentID)
	if err != nil {
		return nil, ErrAgentAccessDenied
	}
	if _, err := c.agentAccess.Use(ctx, authority, agentID); err != nil {
		return nil, ErrAgentAccessDenied
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
		Service:        svc,
		User:           resolved.User,
		AgentID:        agentID,
		SessionKey:     agent.BuildGroupSessionKey(agentID, groupID),
		Channel:        session.Channel(channelCtx),
		ChatCtx:        ChatContext{Platform: msg.Platform, ChannelID: channelID, ChatID: msg.ChatID, GroupID: groupID, IsGroup: true},
		GroupID:        groupID,
		Authority:      authority,
		CurrentSpeaker: platformGroupSpeaker(msg, resolved.User.ID, resolved.User.Name),
	}, nil
}

// platformGroupSpeaker builds the per-turn speaker for a platform group sender.
// A linked sender carries the resolved auth user id (profile target); an unlinked
// sender carries an empty UserID, so no profile is ever injected for them.
func platformGroupSpeaker(msg pkgchannel.IncomingMessage, userID, userName string) memory.CurrentSpeaker {
	displayName := msg.SenderName
	if displayName == "" {
		displayName = userName
	}
	return memory.CurrentSpeaker{
		Platform:       msg.Platform,
		PlatformUserID: msg.SenderID,
		DisplayName:    displayName,
		UserID:         userID,
	}
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

// imageContentPlaceholder is the deterministic text projection stored for a
// group message that carries images but no text (see contentBlocksToText).
const imageContentPlaceholder = "[image]"

// contentBlocksToText projects content blocks onto the plain-text `content`
// column that the semantic arbiter and history assembly read (rehydration for
// dispatch goes through content_blocks instead).
//
// An image-only message has no text blocks, so a naive projection stores "".
// But LLMSemanticGroupArbiter.Decide treats an empty message as "nothing to
// route" and returns silence, so without an explicit @mention a pure-image
// group message would never dispatch — and the content_blocks rehydration path
// would never run. To honor that arbiter/history contract, project an
// image-only message to a fixed "[image]" placeholder (one per message,
// regardless of image count) so the arbiter sees a non-empty message and
// history assembly gets a meaningful line. The placeholder lives only in this
// text projection; groupMessageContentBlocks prefers content_blocks, so it
// never leaks into the rehydrated image blocks the agent actually sees.
func contentBlocksToText(blocks []ai.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if tc, ok := b.(ai.TextContent); ok && tc.Text != "" {
			parts = append(parts, tc.Text)
		}
	}
	if len(parts) == 0 && ai.HasImage(blocks) {
		return imageContentPlaceholder
	}
	return strings.Join(parts, "\n")
}

// marshalGroupContentBlocks serializes message content for event-log storage
// when it carries more than text. Text-only messages store nothing and replay
// from the text projection; a marshal failure degrades the same way rather
// than dropping the message.
func marshalGroupContentBlocks(blocks []ai.ContentBlock) []byte {
	if !ai.HasImage(blocks) {
		return nil
	}
	data, err := ai.MarshalContentBlocks(blocks)
	if err != nil {
		return nil
	}
	return data
}

// FuncGroupMemberLister adapts a function to GroupMemberLister.
type FuncGroupMemberLister func(ctx context.Context, groupID string) ([]GroupMember, error)

func (f FuncGroupMemberLister) ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	return f(ctx, groupID)
}
