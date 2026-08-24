package channel

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// GroupMember represents a bot's membership in a group chat.
type GroupMember struct {
	AgentID        string
	ReplyChannelID string
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
	// Identity first: the stored text names Stella agents, and the wake fan-out
	// reads the same resolved mentions out of the outbox envelope.
	c.resolveMentionAgents(ctx, msg.Platform, msg.Mentions)
	msg.Content = c.rewriteMentionsToAgentNames(ctx, msg.Mentions, msg.Content)
	// A platform whose reply authority is a short-lived ingress secret (DingTalk
	// session webhook) must persist it encrypted at acceptance: the executing
	// replica may not be the one that received the delivery.
	capabilityID := ""
	capabilityCiphertext := ""
	if capability := msg.ReplyCapability; capability != nil {
		if c.vaultSvc == nil {
			return eventlog.AppendResult{}, errors.New("durable reply capability encryption is unavailable")
		}
		if capability.Kind == "" || capability.Secret == "" || !capability.ExpiresAt.After(time.Now().UTC()) {
			return eventlog.AppendResult{}, errors.New("invalid or expired reply capability")
		}
		var err error
		capabilityCiphertext, err = c.vaultSvc.EncryptSystem(capability.Secret)
		if err != nil {
			return eventlog.AppendResult{}, fmt.Errorf("encrypt reply capability: %w", err)
		}
		capabilityID = uuid.Must(uuid.NewV7()).String()
	}
	event, err := c.groupEventMessage(ctx, msg)
	if err != nil {
		return eventlog.AppendResult{}, err
	}
	options := make([]eventlog.AppendOption, 0, 2)
	if attach, err := c.immutableGroupAttachmentOption(ctx, msg); err != nil {
		return eventlog.AppendResult{}, err
	} else if attach != nil {
		options = append(options, attach)
	}
	options = append(options, eventlog.WithOnInserted(func(ctx context.Context, q *sqlc.Queries, result eventlog.AppendResult) error {
		members, err := q.ListGroupMembers(ctx, result.GroupID)
		if err != nil {
			return fmt.Errorf("list group members: %w", err)
		}
		groupMembers := make([]GroupMember, len(members))
		for i, m := range members {
			groupMembers[i] = GroupMember{AgentID: m.AgentID, ReplyChannelID: m.ReplyChannelID}
		}
		c.clearNonMemberMentions(msg.Platform, msg.Mentions, groupMembers)
		msg.Mentions = mergeResolvedMentions(msg.Mentions, parseGroupMentions(ctx, q, contentBlocksToText(msg.Content), members))
		if capabilityID != "" {
			channelID := msg.ChannelID
			if channelID == "" {
				channelID = msg.Platform
			}
			if _, err := q.CreateChannelReplyCapability(ctx, sqlc.CreateChannelReplyCapabilityParams{
				ID: capabilityID, ChannelID: channelID, Kind: msg.ReplyCapability.Kind,
				Ciphertext: capabilityCiphertext,
				ExpiresAt:  msg.ReplyCapability.ExpiresAt.UTC(),
			}); err != nil {
				return fmt.Errorf("persist encrypted reply capability: %w", err)
			}
		}
		envelope, err := EncodeGroupOutboxEnvelopeWithCapability(msg.Mentions, msg.LifecycleFeedback, capabilityID)
		if err != nil {
			return fmt.Errorf("encode outbox envelope: %w", err)
		}
		if err := createPendingGroupOutbox(ctx, q, result.Message.ID, result.GroupID, envelope); err != nil {
			return fmt.Errorf("create group outbox: %w", err)
		}
		return nil
	}))
	return c.eventLog.AppendGroupMessage(ctx, event, options...)
}

// immutableGroupAttachmentOption makes expiring platform attachment bytes
// durable inside the event-log append transaction, so the adapter can never
// acknowledge a delivery whose bytes are unreplayable. Text-only deliveries get
// no option and keep the legacy projection path.
func (c *Coordinator) immutableGroupAttachmentOption(ctx context.Context, msg pkgchannel.IncomingMessage) (eventlog.AppendOption, error) {
	if !ai.HasAttachment(msg.Content) {
		return nil, nil
	}
	candidates := orderedIDs(msg.SenderID)
	if len(msg.SenderIDs) > 0 {
		candidates = orderedIDs(append([]string{msg.SenderID}, msg.SenderIDs...)...)
	}
	resolved, _, err := ResolveUserCandidates(ctx, c.auth, msg.Platform, candidates)
	if err != nil {
		return nil, fmt.Errorf("resolve immutable group attachment owner: %w", err)
	}
	return eventlog.WithBeforeInsert(func(ctx context.Context, q *sqlc.Queries, event *eventlog.Message) error {
		content, _, _, err := c.immutableChannelContentWithQueries(ctx, q, resolved.User.ID, "", msg.Content)
		if err != nil {
			return err
		}
		if err := ai.ValidateCanonicalContentBlocks(content); err != nil {
			return fmt.Errorf("validate durable group content: %w", err)
		}
		event.Content = contentBlocksToText(content)
		event.ContentBlocks = marshalGroupContentBlocks(content)
		return nil
	}), nil
}

// ImportGroupHistory appends platform history as canonical context without
// creating outbox work. Platform message IDs make repeated lazy imports safe.
func (c *Coordinator) ImportGroupHistory(ctx context.Context, messages []pkgchannel.IncomingMessage) error {
	if c.eventLog == nil {
		return errors.New("group event log is not configured")
	}
	for _, msg := range messages {
		if !msg.IsGroup || msg.MessageID == "" {
			return errors.New("group history message is missing group identity")
		}
		event, err := c.groupEventMessage(ctx, msg)
		if err != nil {
			return err
		}
		var options []eventlog.AppendOption
		attach, err := c.immutableGroupAttachmentOption(ctx, msg)
		if err != nil {
			return fmt.Errorf("append imported group history: %w", err)
		}
		if attach != nil {
			options = append(options, attach)
		}
		if _, err := c.eventLog.AppendGroupMessage(ctx, event, options...); err != nil {
			return fmt.Errorf("append imported group history: %w", err)
		}
	}
	return nil
}

func (c *Coordinator) groupEventMessage(ctx context.Context, msg pkgchannel.IncomingMessage) (eventlog.Message, error) {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	if _, err := validatePlatformChannel(ctx, c.store, msg.Platform, channelID); err != nil {
		return eventlog.Message{}, fmt.Errorf("validate source channel: %w", err)
	}
	content := legacyGroupContent(msg.Content)
	return eventlog.Message{
		Platform:          msg.Platform,
		PlatformGroupID:   msg.ChatID,
		PlatformThreadID:  msg.ThreadID,
		SourceChannelID:   channelID,
		ActorType:         eventlog.ActorHuman,
		ActorID:           msg.SenderID,
		ActorDisplayName:  msg.SenderName,
		PlatformMessageID: msg.MessageID,
		PlatformTimestamp: msg.Timestamp,
		ReplyTo:           msg.ReplyTo,
		Content:           contentBlocksToText(content),
		ContentBlocks:     marshalGroupContentBlocks(content),
	}, nil
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
		CurrentSpeaker: platformGroupSpeaker(msg, resolved.User.ID, resolved.User.Name, c.transcriptSpeakerName(ctx, groupID, msg.SenderID)),
	}, nil
}

// transcriptSpeakerName is the name the injected transcript will print for this
// sender, or "" when the namer has none and would fall back to the raw actor id.
func (c *Coordinator) transcriptSpeakerName(ctx context.Context, groupID, senderID string) string {
	if c.db == nil || senderID == "" {
		return ""
	}
	name := eventlog.NewParticipantNamer(sqlc.New(c.db)).Name(ctx, groupID, string(eventlog.ActorHuman), senderID)
	if name == senderID {
		return ""
	}
	return name
}

// platformGroupSpeaker builds the per-turn speaker for a platform group sender.
// A linked sender carries the resolved auth user id (profile target); an unlinked
// sender carries an empty UserID, so no profile is ever injected for them.
//
// transcriptName wins when the namer produced one, so a person is spelled the
// same way in <current_speaker> as on their own transcript line -- the whole
// point of routing every participant name through one function. When the namer
// has nothing and would print the raw actor id, the live platform sender name is
// the friendlier choice and cannot contradict a name the transcript never shows.
func platformGroupSpeaker(msg pkgchannel.IncomingMessage, userID, userName, transcriptName string) memory.CurrentSpeaker {
	displayName := transcriptName
	if displayName == "" {
		displayName = msg.SenderName
	}
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

// legacyGroupContent keeps the old inline group codec bounded at its storage
// boundary. Producers do not need to know whether a message will become an
// ordinary session or a deferred group event.
func legacyGroupContent(blocks []ai.ContentBlock) []ai.ContentBlock {
	out := ai.CloneContentBlocks(blocks)
	for i, block := range out {
		image, ok := block.(ai.ImageContent)
		if !ok {
			continue
		}
		if len(image.Data) > base64.StdEncoding.EncodedLen(vision.MaxRendererPayloadBytes) {
			out[i] = ai.TextContent{Text: ai.UnavailableImageProjection}
			continue
		}
		data, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil || len(data) > vision.MaxRendererPayloadBytes {
			out[i] = ai.TextContent{Text: ai.UnavailableImageProjection}
		}
	}
	return out
}

// imageContentPlaceholder is the deterministic text projection stored for a
// group message that carries images but no text (see contentBlocksToText).
const imageContentPlaceholder = "[image]"

// contentBlocksToText projects content blocks onto the plain-text `content`
// column that the group triage and history assembly read (rehydration for
// dispatch goes through content_blocks instead).
//
// An image-only message has no text blocks, so a naive projection stores "".
// But group triage treats an empty message as "nothing to
// route" and returns silence, so without an explicit @mention a pure-image
// group message would never dispatch — and the content_blocks rehydration path
// would never run. To honor that triage/history contract, project an
// image-only message to a fixed "[image]" placeholder (one per message,
// regardless of image count) so triage sees a non-empty message and
// history assembly gets a meaningful line. The placeholder lives only in this
// text projection; groupMessageContentBlocks prefers content_blocks, so it
// never leaks into the rehydrated image blocks the agent actually sees.
func contentBlocksToText(blocks []ai.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch value := b.(type) {
		case ai.TextContent:
			if value.Text != "" {
				parts = append(parts, value.Text)
			}
		case ai.FileRefContent:
			parts = append(parts, "[file]")
		case ai.FileContent:
			parts = append(parts, "[file]")
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
	if !ai.HasAttachment(blocks) {
		return nil
	}
	data, err := ai.MarshalContentBlocks(blocks)
	if err != nil {
		return nil
	}
	return data
}
