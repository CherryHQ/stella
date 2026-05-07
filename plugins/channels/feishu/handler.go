package feishu

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
)

// onReaction handles incoming Feishu reaction events.
// When a user reacts to a message, the bot sends a text description of the
// reaction to the agent, allowing it to respond if appropriate.
func (b *Bot) onReaction(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
	if event == nil || event.Event == nil {
		return nil
	}

	data := event.Event

	// Determine operator open_id.
	operatorType := derefStr(data.OperatorType)
	if operatorType == "app" {
		// Ignore reactions from apps (including ourselves).
		return nil
	}

	senderIDs := senderIDsFromUserID(data.UserId)
	openID := ""
	if data.UserId != nil {
		openID = derefStr(data.UserId.OpenId)
	}
	if len(senderIDs) == 0 {
		return nil
	}

	// Filter self-reactions.
	if botID, _ := b.botOpenID.Load().(string); botID != "" && openID == botID {
		return nil
	}

	messageID := derefStr(data.MessageId)
	if messageID == "" {
		return nil
	}

	var emojiType string
	if data.ReactionType != nil && data.ReactionType.EmojiType != nil {
		emojiType = *data.ReactionType.EmojiType
	}
	if emojiType == "" {
		return nil
	}

	// Look up the reacted message to get its chat context so we resolve
	// against the correct session (group vs private, threaded vs not).
	chatID, chatType, rootID := b.getMessageContext(messageID)
	if chatType == "group" {
		if b.groupMode(chatID) == "disabled" {
			return nil
		}

		// Append a synthetic entry to the group log.
		b.groupLog(chatID).Append(GroupEntry{
			Timestamp: time.Now(),
			SenderID:  openID,
			Name:      b.cachedName(openID),
			Text:      fmt.Sprintf("[reacted with %s]", emojiType),
			MessageID: messageID,
		})
	}

	// Auto-provision the reacting user. TenantKey is not available in reaction
	// events; the contact API failure acts as the implicit tenant filter.
	// Run synchronously so the user exists before HandleIncoming does the identity lookup.
	if data.UserId != nil {
		unionID := derefStr(data.UserId.UnionId)
		provCtx, provCancel := b.apiContext()
		b.maybeAutoProvision(provCtx, openID, unionID, "")
		provCancel()
	}

	reactionText := fmt.Sprintf("[User reacted with %s on message %s]", emojiType, messageID)
	reactionContent := channel.TextContent(reactionText)

	if chatType == "group" {
		reactionContent = b.attributeGroupContent(openID, reactionContent)
	}

	msg := b.incomingMsg(senderIDs, chatID, chatType, reactionContent)
	replyFn := func(reply string) {
		replyCtx, cancel := b.apiContext()
		defer cancel()
		b.replyInThread(replyCtx, messageID, rootID, reply)
	}

	if chatType == "group" {
		go b.handleIncoming(b.decorateGroupCtx(chatID, messageID, rootID, ""), msg, "", "", msg.SenderID, chatID, messageID, rootID, replyFn)
	} else {
		go b.handleIncoming(context.Background(), msg, "", "", msg.SenderID, chatID, messageID, rootID, replyFn)
	}
	return nil
}

// onMessage handles incoming Feishu messages.
func (b *Bot) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}

	msg := event.Event.Message
	sender := event.Event.Sender

	if sender == nil || sender.SenderId == nil {
		return nil
	}

	senderIDs := senderIDsFromUserID(sender.SenderId)
	if len(senderIDs) == 0 {
		return nil
	}

	openID := derefStr(sender.SenderId.OpenId)

	if botID, _ := b.botOpenID.Load().(string); botID != "" && openID == botID {
		return nil
	}

	chatID := derefStr(msg.ChatId)
	chatType := derefStr(msg.ChatType)
	messageID := derefStr(msg.MessageId)
	rootID := derefStr(msg.RootId)
	mentions := msg.Mentions

	if messageID != "" && b.markSeen(messageID) {
		logger().Debug("duplicate message ignored", "message_id", messageID)
		return nil
	}

	var triggerGroupCtx string
	if chatType == "group" {
		// Disabled groups: ignore entirely.
		if b.groupMode(chatID) == "disabled" {
			return nil
		}

		plainText := parseTextContent(derefStr(msg.Content))
		plainText = stripMentions(plainText, mentions)
		entry := GroupEntry{
			Timestamp: time.Now(),
			SenderID:  openID,
			Name:      b.cachedName(openID),
			Text:      plainText,
			MessageID: messageID,
		}

		// Non-trigger messages: append to log for context and return.
		if !b.isGroupTrigger(chatID, mentions) {
			b.groupLog(chatID).Append(entry)
			return nil
		}

		// Trigger message: capture context before appending so the trigger
		// message doesn't appear in both the system prompt and user message.
		gl := b.groupLog(chatID)
		triggerGroupCtx = gl.FormatContext(50, b.cachedName)
		gl.Append(entry)
	}

	// Extract text once for commands.
	text := parseTextContent(derefStr(msg.Content))
	if chatType == "group" {
		text = stripMentions(text, mentions)
	}

	// Auto-provision the sender if enabled. Done after dedup+group checks so
	// duplicate events and bots in disabled groups never trigger provisioning.
	// Skip for /link so the existing manual-link flow stays authoritative.
	// Run synchronously so the user exists before HandleIncoming does the identity lookup.
	if cmd0, _ := channel.ParseSlashCommand(text); cmd0 != "/link" {
		unionID := derefStr(sender.SenderId.UnionId)
		tenantKey := derefStr(sender.TenantKey)
		provCtx, provCancel := b.apiContext()
		b.maybeAutoProvision(provCtx, openID, unionID, tenantKey)
		provCancel()
	}

	// For file messages: resolve the per-user assets directory before building
	// content so the downloaded file lands in the user's persistent workspace
	// rather than a throwaway temp directory.
	var assetsDir string
	if derefStr(msg.MessageType) == "file" {
		if resolver, ok := b.handler.(channel.UserRootResolver); ok {
			probeMsg := b.incomingMsg(senderIDs, chatID, chatType, nil)
			resolveCtx, resolveCancel := b.apiContext()
			if userRoot, err := resolver.ResolveUserRoot(resolveCtx, probeMsg); err == nil {
				assetsDir = agent.UserAssetsDir(userRoot)
			} else {
				logger().Warn("resolve user root failed, file will use placeholder", "error", err)
			}
			resolveCancel()
		}
	}

	content := b.buildMessageContent(msg, assetsDir)
	if content == nil {
		return nil
	}

	replyFn := func(reply string) {
		replyCtx, cancel := b.apiContext()
		defer cancel()
		b.replyInThread(replyCtx, messageID, rootID, reply)
	}

	// For group messages, attribute content and build a decorated context
	// carrying the group log and reply callback.
	var opCtx context.Context
	if chatType == "group" {
		opCtx = b.decorateGroupCtx(chatID, messageID, rootID, triggerGroupCtx)
		content = b.attributeGroupContent(openID, content)
	}

	incoming := b.incomingMsg(senderIDs, chatID, chatType, content)

	// Handle plugin-local commands first.
	if text != "" {
		cmd, args := channel.ParseSlashCommand(text)
		switch cmd {
		case "/auth":
			provider := "feishu"
			if args != "" {
				provider = strings.TrimSpace(args)
			}
			authContent := channel.TextContent(fmt.Sprintf("Please connect my %s OAuth credentials using the oauth tool with action=connect and provider=%s. Show me the verification URL so I can authorize in my browser.", provider, provider))
			authMsg := b.incomingMsg(senderIDs, chatID, chatType, authContent)
			if chatType == "group" {
				go b.handleIncoming(opCtx, authMsg, "", "", authMsg.SenderID, chatID, messageID, rootID, replyFn)
			} else {
				go b.handleIncoming(context.Background(), authMsg, "", "", authMsg.SenderID, chatID, messageID, rootID, replyFn)
			}
			return nil
		case "/model":
			b.handleModelCommand(args, replyFn)
			return nil
		case "/agent":
			b.handleAgentCommand(incoming, args, replyFn)
			return nil
		}
	}

	// Parse command for coordinator (shared commands + chat streaming).
	cmd, args := channel.ParseSlashCommand(text)
	if chatType == "group" {
		go b.handleIncoming(opCtx, incoming, cmd, args, incoming.SenderID, chatID, messageID, rootID, replyFn)
	} else {
		go b.handleIncoming(context.Background(), incoming, cmd, args, incoming.SenderID, chatID, messageID, rootID, replyFn)
	}
	return nil
}

// buildMessageContent constructs the message content from a Feishu message.
// assetsDir is the resolved per-user assets directory; pass "" to fall back to
// the filename-only placeholder when the path is not yet known.
func (b *Bot) buildMessageContent(msg *larkim.EventMessage, assetsDir string) []ai.ContentBlock {
	msgType := derefStr(msg.MessageType)
	rawContent := derefStr(msg.Content)
	messageID := derefStr(msg.MessageId)

	switch msgType {
	case "text":
		text := parseTextContent(rawContent)
		text = stripMentions(text, msg.Mentions)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return channel.TextContent(text)

	case "post":
		if strings.TrimSpace(rawContent) == "" {
			return nil
		}
		return channel.TextContent(rawContent)

	case "image":
		imageKey := extractJSONField(rawContent, "image_key")
		if imageKey == "" {
			logger().Warn("image message missing image_key")
			return nil
		}
		data, mime, err := b.downloadImage(messageID, imageKey)
		if err != nil {
			logger().Error("download image failed", "image_key", imageKey, "error", err)
			return nil
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		logger().Debug("image received", "size", len(data), "mime", mime)
		return []ai.ContentBlock{
			ai.ImageContent{Data: encoded, MimeType: mime},
		}

	case "audio":
		return channel.TextContent(parseAudioContent(rawContent))

	case "video", "media":
		return channel.TextContent(parseVideoContent(rawContent))

	case "file":
		fileKey := extractJSONField(rawContent, "file_key")
		fileName := extractJSONField(rawContent, "file_name")
		if fileName == "" {
			fileName = "file"
		}
		if fileKey != "" && assetsDir != "" {
			path, err := b.downloadFile(messageID, fileKey, fileName, assetsDir)
			if err != nil {
				logger().Error("download file failed", "file_key", fileKey, "error", err)
			} else {
				return channel.FileReceivedContent(fileName, assetsDir, path)
			}
		}
		return channel.TextContent(parseFileContent(rawContent))

	case "sticker":
		return channel.TextContent(parseStickerContent(rawContent))

	case "location":
		return channel.TextContent(parseLocationContent(rawContent))

	case "share_chat":
		return channel.TextContent(parseShareChatContent(rawContent))

	case "share_user":
		return channel.TextContent(parseShareUserContent(rawContent))

	case "merge_forward":
		return channel.TextContent(parseMergeForwardContent(rawContent))

	default:
		logger().Debug("unsupported message type", "type", msgType)
		return channel.TextContent(fmt.Sprintf("[Unsupported message type: %s]", msgType))
	}
}

// handleIncoming delegates to the coordinator via HandleIncoming.
// Shared commands are handled by the coordinator (including /abort);
// otherwise a chat stream is returned.
func (b *Bot) handleIncoming(ctx context.Context, msg channel.IncomingMessage, cmd, args, senderID, chatID, messageID, rootID string, replyFn func(string)) {
	// Ack with 🤔 so the user sees the bot is processing.
	ackReactionID := b.reactToMessage(messageID, reactionAck)

	// Wrap the incoming context with an operation timeout so in-flight work
	// has bounded execution time while preserving any context values (e.g.
	// group context, reply fn).
	ctx, cancel := context.WithTimeout(ctx, feishuOperationTimeout)

	resp, handled, stream, err := b.handler.HandleIncoming(ctx, msg, cmd, args)
	if err != nil {
		cancel()
		if ackReactionID != "" {
			b.removeReaction(messageID, ackReactionID)
		}
		b.reactToMessage(messageID, reactionError)
		logger().Error("chat failed", "sender_id", senderID, "error", err)
		replyCtx, cancel := b.apiContext()
		defer cancel()
		b.replyInThread(replyCtx, messageID, rootID, fmt.Sprintf("Session error: %v", err))
		return
	}
	if handled {
		defer cancel()
		if ackReactionID != "" {
			b.removeReaction(messageID, ackReactionID)
		}
		replyFn(resp)
		return
	}
	defer cancel()

	logger().Debug("message received", "sender_id", senderID, "session", stream.SessionID, "root_id", rootID)

	sentMsgID, response, images, files, elapsed, streamErr := b.streamResponseInThread(stream.Events, chatID, messageID, rootID, msg.IsGroup)

	if ackReactionID != "" {
		b.removeReaction(messageID, ackReactionID)
	}

	if streamErr != nil {
		logger().Error("agent stream error", "session_id", stream.SessionID, "error", streamErr)
		b.reactToMessage(messageID, reactionError)
		if response == "" {
			response = fmt.Sprintf("Agent error: %v", streamErr)
		} else {
			response += fmt.Sprintf("\n\n[Agent error: %v]", streamErr)
		}
	}

	if strings.TrimSpace(response) == "" {
		if msg.IsGroup {
			// In group mode the agent may choose not to respond; stay silent.
		} else {
			response = "(empty response)"
		}
	}
	if strings.TrimSpace(response) != "" {
		finalResponse := response + elapsedFooter(elapsed)
		b.sendFinalResponseInThread(chatID, messageID, rootID, sentMsgID, finalResponse)
	}

	for _, img := range images {
		b.sendImageInThread(chatID, messageID, rootID, img)
	}

	for _, file := range files {
		b.sendFileInThread(chatID, messageID, rootID, file)
	}

	logger().Debug("response sent", "sender_id", senderID, "response_len", len(response), "images", len(images))
}

// replyText sends a text reply to a message.
func (b *Bot) replyText(ctx context.Context, messageID, text string) {
	content := textContent(text)
	resp, err := b.client.Im.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
				Content(content).
				Build()).
			Build())
	if err != nil {
		logger().Error("reply failed", "message_id", messageID, "error", err)
		return
	}
	if !resp.Success() {
		logger().Error("reply failed", "message_id", messageID, "code", resp.Code, "msg", resp.Msg)
	}
}

// getMessageContext fetches a message's chat_id, chat_type, and root_id via
// the Get Message API. Returns ("", "p2p", "") on any failure so the caller
// falls back to a private session rather than leaking across sessions.
func (b *Bot) getMessageContext(messageID string) (chatID, chatType, rootID string) {
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.Message.Get(apiCtx,
		larkim.NewGetMessageReqBuilder().
			MessageId(messageID).
			Build())
	if err != nil || !resp.Success() || resp.Data == nil || len(resp.Data.Items) == 0 {
		return "", "p2p", ""
	}
	msg := resp.Data.Items[0]
	chatID = derefStr(msg.ChatId)
	rootID = derefStr(msg.RootId)
	// Message API doesn't return chat_type; derive from chat_id prefix.
	if strings.HasPrefix(chatID, "oc_") {
		chatType = "group"
	} else {
		chatType = "p2p"
	}
	return chatID, chatType, rootID
}

// derefStr safely dereferences a string pointer, returning empty string if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
