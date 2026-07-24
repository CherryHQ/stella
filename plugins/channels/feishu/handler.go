package feishu

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
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

	msg := b.incomingMsg(senderIDs, chatID, chatType, channel.TextContent(reactionText))
	msg.ThreadID = rootID
	replyFn := func(reply string) {
		replyCtx, cancel := b.apiContext()
		defer cancel()
		b.replyInThread(replyCtx, messageID, rootID, reply)
	}
	go b.handleIncoming(msg, "", "", msg.SenderID, chatID, messageID, rootID, replyFn)
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
	if unionID := derefStr(sender.SenderId.UnionId); openID != "" && unionID != "" {
		b.unionIDs.Store(openID, unionID)
	}

	if botID, _ := b.botOpenID.Load().(string); botID != "" && openID == botID {
		return nil
	}

	chatID := derefStr(msg.ChatId)
	chatType := derefStr(msg.ChatType)
	messageID := derefStr(msg.MessageId)
	rootID := derefStr(msg.RootId)
	mentions := msg.Mentions

	if chatID != "" && chatType != "" {
		b.chatTypes.Store(chatID, chatType)
	}

	if messageID != "" && b.markSeen(messageID) {
		logger().Debug("duplicate message ignored", "message_id", messageID)
		return nil
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

	// For file and image-bearing messages: resolve the per-user assets directory
	// before building content so the downloaded attachment lands in the user's
	// persistent workspace rather than a throwaway temp directory.
	var assetsDir string
	switch derefStr(msg.MessageType) {
	case "file", "image", "post":
		if resolver, ok := b.handler.(channel.UserRootResolver); ok {
			probeMsg := b.incomingMsg(senderIDs, chatID, chatType, nil)
			probeMsg.ThreadID = rootID
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

	// Prepend per-group system prompt to content if configured.
	if chatType == "group" {
		if sp := b.groupSystemPrompt(chatID); sp != "" {
			content = prependSystemPrompt(content, sp)
		}
	}

	incoming := b.incomingMsg(senderIDs, chatID, chatType, content)
	incoming.ThreadID = rootID
	incoming.MessageID = messageID
	incoming.ReplyTo = derefStr(msg.ParentId)
	incoming.Timestamp = feishuEventTime(derefStr(msg.CreateTime))
	incoming.Mentions = feishuMentions(mentions)

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
			authMsg.ThreadID = rootID
			authMsg.MessageID = messageID
			authMsg.ReplyTo = derefStr(msg.ParentId)
			authMsg.Timestamp = feishuEventTime(derefStr(msg.CreateTime))
			authMsg.Mentions = feishuMentions(mentions)
			go b.handleIncoming(authMsg, "", "", authMsg.SenderID, chatID, messageID, rootID, replyFn)
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
	go b.handleIncoming(incoming, cmd, args, incoming.SenderID, chatID, messageID, rootID, replyFn)
	return nil
}

func (b *Bot) onMessageRead(_ context.Context, _ *larkim.P2MessageReadV1) error {
	return nil
}

func (b *Bot) onReactionDeleted(_ context.Context, _ *larkim.P2MessageReactionDeletedV1) error {
	return nil
}

// prependSystemPrompt adds a system prompt prefix to message content.
func prependSystemPrompt(content []ai.ContentBlock, prompt string) []ai.ContentBlock {
	prefix := ai.TextContent{Text: fmt.Sprintf("[System: %s]", prompt)}
	return append([]ai.ContentBlock{prefix}, content...)
}

// imageContentBlocks downloads an image by key, persists it to the user's
// assets when possible, and returns the unified attachment blocks. Without an
// assets dir (or when saving fails) the image degrades to inline-only. The
// bool is false when the download fails (already logged), letting callers
// decide whether to drop the message or emit a text fallback.
func (b *Bot) imageContentBlocks(messageID, imageKey, assetsDir string) ([]ai.ContentBlock, bool) {
	data, mime, err := b.downloadImage(messageID, imageKey)
	if err != nil {
		logger().Error("download image failed", "image_key", imageKey, "error", err)
		return nil, false
	}
	logger().Debug("image received", "size", len(data), "mime", mime)
	fileName := channel.ImageFileName(imageKey, mime)
	if assetsDir != "" {
		savedPath, saveErr := b.saveAsset(b.ctx, assetsDir, fileName, data)
		if saveErr == nil {
			return channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data), true
		}
		logger().Warn("save inbound image failed", "error", saveErr)
	}
	return []ai.ContentBlock{ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime}}, true
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
		text, imageKeys := parsePostBlocks(rawContent)
		text = stripMentions(text, msg.Mentions)
		if len(imageKeys) == 0 {
			if strings.TrimSpace(text) == "" {
				logger().Warn("post message produced no usable content", "message_id", messageID)
				return nil
			}
			return channel.TextContent(text)
		}
		// Build mixed content: text + downloaded images.
		var blocks []ai.ContentBlock
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, ai.TextContent{Text: text})
		}
		for _, imgKey := range imageKeys {
			imgBlocks, ok := b.imageContentBlocks(messageID, imgKey, assetsDir)
			if !ok {
				blocks = append(blocks, ai.TextContent{Text: fmt.Sprintf("[Failed to download image: %s]", imgKey)})
				continue
			}
			blocks = append(blocks, imgBlocks...)
		}
		if len(blocks) == 0 {
			return nil
		}
		return blocks

	case "image":
		imageKey := extractJSONField(rawContent, "image_key")
		if imageKey == "" {
			logger().Warn("image message missing image_key")
			return nil
		}
		blocks, ok := b.imageContentBlocks(messageID, imageKey, assetsDir)
		if !ok {
			return channel.TextContent("[Failed to download image]")
		}
		return blocks

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
			path, data, err := b.downloadFile(messageID, fileKey, fileName, assetsDir)
			if err != nil {
				logger().Error("download file failed", "file_key", fileKey, "error", err)
			} else {
				return channel.AttachmentReceivedContent(fileName, assetsDir, path, data)
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
func (b *Bot) handleIncoming(msg channel.IncomingMessage, cmd, args, senderID, chatID, messageID, rootID string, replyFn func(string)) {
	// Ack immediately: user sees 🤔 while the bot processes.
	ackReactionID := b.reactToMessage(messageID, reactionAck)

	// Use an operation-scoped context so in-flight work survives bot restarts
	// with bounded execution time. Keep it alive for the full streamed turn;
	// cancelling immediately after HandleIncoming returns would abort the agent
	// stream and release the per-session queue too early.
	ctx, cancel := b.operationContext()

	resp, handled, stream, err := b.handler.HandleIncoming(ctx, msg, cmd, args)
	if err != nil {
		cancel()
		b.removeReaction(messageID, ackReactionID)
		b.reactToMessage(messageID, reactionError)
		logger().Error("chat failed", "sender_id", senderID, "error", err)
		replyCtx, cancel := b.apiContext()
		defer cancel()
		b.replyInThread(replyCtx, messageID, rootID, fmt.Sprintf("Session error: %v", err))
		return
	}
	if handled {
		defer cancel()
		b.removeReaction(messageID, ackReactionID)
		replyFn(resp)
		return
	}
	if stream == nil {
		defer cancel()
		b.removeReaction(messageID, ackReactionID)
		return
	}
	defer cancel()

	logger().Debug("message received", "sender_id", senderID, "session", stream.SessionID, "root_id", rootID)

	sentMsgID, response, images, files, refs, elapsed, streamErr := b.streamResponseInThread(stream.Events, chatID, messageID, rootID)

	b.removeReaction(messageID, ackReactionID)

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
		response = "(empty response)"
	}

	// Append elapsed time footer to the final response.
	finalResponse := response + elapsedFooter(elapsed)

	b.sendFinalResponseInThread(chatID, messageID, rootID, sentMsgID, finalResponse, refs, msg.IsGroup)

	for _, img := range images {
		b.sendImageInThread(chatID, messageID, rootID, img)
	}

	for _, file := range files {
		b.sendFileInThread(chatID, messageID, rootID, file)
	}

	logger().Debug("response sent", "sender_id", senderID, "response_len", len(response), "images", len(images))
}

// replyText sends a text reply to a message. When the text contains
// {{button ...}} directives it sends an interactive card — but only if the card
// actually builds. If the card build fails it degrades to genuine plain text
// (directives stripped) rather than sending an interactive type with text-shaped
// content, which Feishu rejects.
func (b *Bot) replyText(ctx context.Context, messageID, text string) {
	msgType := larkim.MsgTypeText
	content := textContent(text)
	if cardButtonDirective.MatchString(text) {
		if card, err := buildCardContent(text); err == nil {
			msgType = larkim.MsgTypeInteractive
			content = card
		} else {
			content = textContent(stripCardDirectives(text))
		}
	}

	resp, err := b.client.Im.Message.Reply(ctx,
		larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(msgType).
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
	chatType = b.getChatType(chatID)
	return chatID, chatType, rootID
}

// resolveUnionID returns the union_id for an open_id. It checks the in-memory
// cache first (populated by onMessage), then falls back to the Contact API.
// Returns "" if the union_id cannot be resolved.
func (b *Bot) resolveUnionID(ctx context.Context, openID string) string {
	if openID == "" {
		return ""
	}
	if v, ok := b.unionIDs.Load(openID); ok {
		return v.(string)
	}
	profile := b.fetchTenantProfile(ctx, openID)
	if profile == nil || profile.UnionID == "" {
		return ""
	}
	b.unionIDs.Store(openID, profile.UnionID)
	return profile.UnionID
}

// getChatType queries the Get Chat API to determine whether a chat is p2p or
// group. Feishu P2P chats also use oc_ prefixed IDs, so prefix-based guessing
// is unreliable.
func (b *Bot) getChatType(chatID string) string {
	if chatID == "" || b.client == nil {
		return "p2p"
	}
	if v, ok := b.chatTypes.Load(chatID); ok {
		return v.(string)
	}
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.Chat.Get(apiCtx,
		larkim.NewGetChatReqBuilder().
			ChatId(chatID).
			Build())
	if err != nil || !resp.Success() || resp.Data == nil {
		return "p2p"
	}
	ct := "group"
	if derefStr(resp.Data.ChatMode) == "p2p" {
		ct = "p2p"
	}
	b.chatTypes.Store(chatID, ct)
	return ct
}

// derefStr safely dereferences a string pointer, returning empty string if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
