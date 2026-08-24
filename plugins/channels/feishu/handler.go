package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	internalchannel "github.com/CherryHQ/stella/internal/channel"
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
	botID, _ := b.botOpenID.Load().(string)
	if botID == "" {
		logger().Warn("rejecting Feishu reaction: bot open_id is unknown")
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
	if openID == botID {
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
	chatID, chatType, rootID, botAuthored, ok := b.resolveMessageContext(messageID)
	directed := botAuthored
	if !ok || !b.admitIngress(chatID, chatType, directed, false) {
		return nil
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

	botID, _ := b.botOpenID.Load().(string)
	if botID == "" {
		logger().Warn("rejecting Feishu message: bot open_id is unknown", "chat_id", derefStr(msg.ChatId))
		return nil
	}
	if openID == botID {
		return nil
	}

	chatID := derefStr(msg.ChatId)
	chatType := normalizeChatType(derefStr(msg.ChatType))
	messageID := derefStr(msg.MessageId)
	rootID := derefStr(msg.RootId)
	parentID := derefStr(msg.ParentId)
	mentions := msg.Mentions
	mentioned := b.isAutoProvisionMessage(chatType, mentions)
	if messageID != "" && b.markSeen(messageID) {
		logger().Debug("duplicate message ignored", "message_id", messageID)
		return nil
	}
	directed := false
	if parentID != "" && chatType == "group" && b.cfg.RequireMention && !mentioned && b.chatAllowed(chatID) {
		parentChatID, _, _, botAuthored, ok := b.resolveMessageContext(parentID)
		directed = ok && parentChatID == chatID && botAuthored
	}
	if !b.admitIngress(chatID, chatType, directed, mentioned) {
		return nil
	}

	if chatID != "" && chatType != "" {
		b.chatTypes.Store(chatID, chatType)
	}

	// Extract text once for commands.
	text := parseTextContent(derefStr(msg.Content))
	if chatType == "group" {
		text = stripMentions(text, mentions)
	}

	// Auto-provision only direct messages or group messages that explicitly
	// mention this bot. Group arbitration happens asynchronously after this
	// boundary, so using its eventual decision here would admit unaddressed
	// group traffic before it is selected. Skip /link to keep manual linking
	// authoritative. Run synchronously so the user exists before identity lookup.
	if cmd0, _ := channel.ParseSlashCommand(text); cmd0 != "/link" && b.isAutoProvisionMessage(chatType, mentions) {
		tenantKey := derefStr(sender.TenantKey)
		provCtx, provCancel := b.apiContext()
		canonicalUnionID := b.maybeAutoProvision(provCtx, openID, tenantKey)
		provCancel()
		if canonicalUnionID != "" {
			senderIDs = feishuSenderIDs(append([]string{canonicalUnionID}, senderIDs...)...)
			if openID != "" {
				b.unionIDs.Store(openID, canonicalUnionID)
			}
		}
	}

	// For file and image-bearing messages: resolve the per-user assets directory
	// before building content so the downloaded attachment lands in the user's
	// persistent workspace rather than a throwaway temp directory.
	var assetMsg channel.IncomingMessage
	switch derefStr(msg.MessageType) {
	case "file", "image", "post", "audio", "video", "media":
		resolver, ok := b.handler.(channel.AssetSaveAdmitter)
		if !ok {
			logger().Warn("rejecting attachment: user root resolver unavailable")
			b.forgetSeen(messageID)
			return nil
		}
		{
			probeMsg := b.incomingMsg(senderIDs, chatID, chatType, nil)
			probeMsg.ThreadID = rootID
			resolveCtx, resolveCancel := b.apiContext()
			err := resolver.AdmitAssetSave(resolveCtx, probeMsg)
			resolveCancel()
			if err != nil {
				logger().Warn("rejecting attachment: resolve user root failed", "error", err)
				b.forgetSeen(messageID)
				replyCtx, cancel := b.apiContext()
				defer cancel()
				text := attachmentRejectionText(err)
				b.replyInThread(replyCtx, messageID, rootID, text)
				return nil
			}
			assetMsg = probeMsg
		}
	}

	content := b.buildMessageContent(msg, assetMsg)
	if content == nil {
		if isFeishuAttachmentType(derefStr(msg.MessageType)) {
			b.forgetSeen(messageID)
		}
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

	if incoming.IsGroup && rootID != "" {
		provisionCtx, provisionCancel := b.apiContext()
		err := b.ensureThreadGroupMember(provisionCtx, chatID, rootID)
		provisionCancel()
		if err != nil {
			b.forgetSeen(messageID)
			return err
		}
	}

	// Handle the plugin-local OAuth shortcut first.
	if text != "" {
		cmd, args := channel.ParseSlashCommand(text)
		if cmd == "/auth" {
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
		}
	}

	// Parse command for coordinator (shared commands + chat streaming).
	cmd, args := channel.ParseSlashCommand(text)
	go b.handleIncoming(incoming, cmd, args, incoming.SenderID, chatID, messageID, rootID, replyFn)
	return nil
}

func isFeishuAttachmentType(messageType string) bool {
	switch messageType {
	case "file", "image", "post", "audio", "video", "media":
		return true
	default:
		return false
	}
}

func attachmentRejectionText(err error) string {
	if errors.Is(err, internalchannel.ErrAgentAccessDenied) || errors.Is(err, agentaccess.ErrForbidden) {
		return "Guest chat currently supports text messages only."
	}
	return "Unable to process this attachment right now."
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
func (b *Bot) imageContentBlocks(messageID, imageKey string, assetMsg channel.IncomingMessage) ([]ai.ContentBlock, bool) {
	data, mime, err := b.downloadImage(messageID, imageKey)
	if err != nil {
		logger().Error("download image failed", "image_key", imageKey, "error", err)
		return nil, false
	}
	logger().Debug("image received", "size", len(data), "mime", mime)
	fileName := channel.ImageFileName(imageKey, mime)
	if assetMsg.Platform != "" {
		savedPath, saveErr := b.saveAsset(b.ctx, assetMsg, fileName, data)
		if saveErr == nil {
			return channel.AttachmentReceivedContent(fileName, savedPath, data), true
		}
		logger().Warn("save inbound image failed", "error", saveErr)
	}
	// Persistence unavailable — degrade to inline within the ceiling; images past
	// the inline limit become an explicit text note instead.
	return channel.InlineImageFallback(fileName, mime, data), true
}

// buildMessageContent constructs the message content from a Feishu message.
// assetsDir is the resolved per-user assets directory; pass "" to fall back to
// the filename-only placeholder when the path is not yet known.
func (b *Bot) buildMessageContent(msg *larkim.EventMessage, assetMsg channel.IncomingMessage) []ai.ContentBlock {
	msgType := derefStr(msg.MessageType)
	rawContent := derefStr(msg.Content)
	messageID := derefStr(msg.MessageId)

	switch msgType {
	case "text":
		text := parseTextContent(rawContent)
		text = renderMentions(text, msg.Mentions)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return channel.TextContent(text)

	case "post":
		if strings.TrimSpace(rawContent) == "" {
			return nil
		}
		text, imageKeys := parsePostBlocks(rawContent)
		text = renderMentions(text, msg.Mentions)
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
			imgBlocks, ok := b.imageContentBlocks(messageID, imgKey, assetMsg)
			if !ok {
				return nil
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
		blocks, ok := b.imageContentBlocks(messageID, imageKey, assetMsg)
		if !ok {
			return nil
		}
		return blocks

	case "audio", "video", "media":
		fileKey := extractJSONField(rawContent, "file_key")
		if fileKey == "" || assetMsg.Platform == "" {
			logger().Warn("attachment message missing file resource", "type", msgType)
			return nil
		}
		fileName := extractJSONField(rawContent, "file_name")
		if fileName == "" {
			fileName = fmt.Sprintf("%s-%s", msgType, fileKey)
		}
		data, err := b.downloadFile(messageID, fileKey)
		if err != nil {
			logger().Error("download media failed", "type", msgType, "file_key", fileKey, "error", err)
			return nil
		}
		savedPath, err := b.saveAsset(b.ctx, assetMsg, fileName, data)
		if err != nil {
			logger().Warn("save inbound media failed", "type", msgType, "error", err)
			return channel.AttachmentSaveFailureContent(fileName, data)
		}
		return channel.AttachmentReceivedContent(fileName, savedPath, data)

	case "file":
		fileKey := extractJSONField(rawContent, "file_key")
		fileName := extractJSONField(rawContent, "file_name")
		if fileName == "" {
			fileName = "file"
		}
		if fileKey != "" && assetMsg.Platform != "" {
			data, err := b.downloadFile(messageID, fileKey)
			if err != nil {
				logger().Error("download file failed", "file_key", fileKey, "error", err)
				return nil
			}
			savedPath, saveErr := b.saveAsset(b.ctx, assetMsg, fileName, data)
			if saveErr != nil {
				// Persistence failed after a successful download — route a fallback
				// to the agent rather than discarding the bytes.
				logger().Warn("save inbound file failed", "error", saveErr)
				return channel.AttachmentSaveFailureContent(fileName, data)
			}
			return channel.AttachmentReceivedContent(fileName, savedPath, data)
		}
		return nil

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
	// Use an operation-scoped context so in-flight work survives bot restarts
	// with bounded execution time. Keep it alive for the full streamed turn;
	// cancelling immediately after HandleIncoming returns would abort the agent
	// stream and release the per-session queue too early.
	ctx, cancel := b.operationContext()
	if ai.HasAttachment(msg.Content) {
		admitter, ok := b.handler.(channel.AttachmentAdmitter)
		if !ok {
			b.forgetSeen(messageID)
			cancel()
			logger().Error("attachment admission unavailable", "sender_id", senderID)
			return
		}
		if err := admitter.AdmitAttachments(ctx, msg); err != nil {
			b.forgetSeen(messageID)
			cancel()
			b.reactToMessage(messageID, reactionError)
			logger().Error("attachment admission failed", "sender_id", senderID, "error", err)
			return
		}
	}
	// The acknowledgement is visible only after expiring attachment bytes and
	// all FIFO quotas have crossed the durable acceptance boundary.
	ackReactionID := b.reactToMessage(messageID, reactionAck)

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

	cancelControl := b.newDirectCancelControl(ctx, msg, senderID)
	sentMsgID, response, images, files, refs, elapsed, streamErr := b.streamResponseInThread(ctx, stream, chatID, messageID, rootID, cancelControl)
	var egressErr streamEgressError
	if errors.As(streamErr, &egressErr) {
		return
	}

	if err := stream.CheckOperation(ctx); err != nil {
		return
	}
	b.removeReaction(messageID, ackReactionID)

	if cancelControl.wasCancelled() {
		response = "⏹️ Cancelled."
		images = nil
		files = nil
	} else if streamErr != nil {
		logger().Error("agent stream error", "session_id", stream.SessionID, "error", streamErr)
		if err := stream.CheckOperation(ctx); err != nil {
			return
		}
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

	if err := b.sendFinalResponseInThread(ctx, chatID, messageID, rootID, sentMsgID, finalResponse, refs, msg.IsGroup, true, stream); err != nil {
		logger().Error("Feishu response delivery failed", "chat_id", chatID, "root_id", rootID, "message_id", messageID, "error", err)
		return
	}

	for _, img := range images {
		if err := b.sendImageInThread(ctx, chatID, messageID, rootID, img, stream); err != nil {
			logger().Error("send response image failed", "message_id", messageID, "error", err)
			return
		}
	}

	for _, file := range files {
		if err := b.sendFileInThread(ctx, chatID, messageID, rootID, file, stream); err != nil {
			logger().Error("send response file failed", "message_id", messageID, "error", err)
			return
		}
	}

	logger().Debug("response sent", "sender_id", senderID, "response_len", len(response), "images", len(images))
}

// replyText sends a text reply to a message. When the text contains
// {{button ...}} directives it sends an interactive card — but only if the card
// actually builds. If the card build fails it degrades to genuine plain text
// (directives stripped) rather than sending an interactive type with text-shaped
// content, which Feishu rejects.
func (b *Bot) replyText(ctx context.Context, messageID, text string, replyInThread bool) error {
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
	if b.client == nil {
		err := errors.New("feishu client is not initialized")
		logger().Error("reply failed", "message_id", messageID, "error", err)
		return err
	}
	err := b.retryFeishuSend(ctx, "reply text", func(ctx context.Context) error {
		resp, err := b.client.Im.Message.Reply(ctx,
			larkim.NewReplyMessageReqBuilder().
				MessageId(messageID).
				Body(replyMessageBody(msgType, content, replyInThread)).
				Build())
		if err != nil {
			return err
		}
		if !resp.Success() {
			return &feishuAPIError{code: resp.Code, msg: resp.Msg}
		}
		return nil
	})
	if err != nil {
		logger().Error("reply failed", "message_id", messageID, "error", err)
	}
	return err
}

func (b *Bot) admitIngress(chatID, chatType string, directed, mentioned bool) bool {
	if directed && chatID == "" {
		return false
	}
	switch chatType {
	case "p2p":
		return b.cfg.AllowDM
	case "group":
		return b.chatAllowed(chatID) && (directed || !b.cfg.RequireMention || mentioned)
	default:
		return false
	}
}

func normalizeChatType(chatType string) string {
	if chatType == "topic" || chatType == "topic_group" {
		return "group"
	}
	return chatType
}

func (b *Bot) resolveMessageContext(messageID string) (chatID, chatType, rootID string, botAuthored, ok bool) {
	if b.resolveMessageContextFn != nil {
		chatID, chatType, rootID, botAuthored, ok = b.resolveMessageContextFn(messageID)
		return chatID, normalizeChatType(chatType), rootID, botAuthored, ok
	}
	return b.getMessageContext(messageID)
}

// getMessageContext fetches authoritative message and chat context. It fails
// closed when either lookup cannot establish the chat ID and type.
func (b *Bot) getMessageContext(messageID string) (chatID, chatType, rootID string, botAuthored, ok bool) {
	if b.client == nil || messageID == "" {
		return "", "", "", false, false
	}
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.Message.Get(apiCtx,
		larkim.NewGetMessageReqBuilder().
			MessageId(messageID).
			Build())
	if err != nil || !resp.Success() || resp.Data == nil || len(resp.Data.Items) == 0 {
		return "", "", "", false, false
	}
	msg := resp.Data.Items[0]
	chatID = derefStr(msg.ChatId)
	rootID = derefStr(msg.RootId)
	botAuthored = isBotAuthoredMessage(msg, b.cfg.AppID)
	chatType, ok = b.getChatType(chatID)
	return chatID, chatType, rootID, botAuthored, ok && chatID != ""
}

func isBotAuthoredMessage(msg *larkim.Message, botAppID string) bool {
	return msg != nil && botAppID != "" && msg.Sender != nil &&
		derefStr(msg.Sender.SenderType) == "app" && derefStr(msg.Sender.IdType) == "app_id" &&
		derefStr(msg.Sender.Id) == botAppID
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
func (b *Bot) getChatType(chatID string) (string, bool) {
	if chatID == "" {
		return "", false
	}
	if v, ok := b.chatTypes.Load(chatID); ok {
		chatType, valid := v.(string)
		chatType = normalizeChatType(chatType)
		return chatType, valid && (chatType == "p2p" || chatType == "group")
	}
	if b.client == nil {
		return "", false
	}
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.Chat.Get(apiCtx,
		larkim.NewGetChatReqBuilder().
			ChatId(chatID).
			Build())
	if err != nil || !resp.Success() || resp.Data == nil {
		return "", false
	}
	var ct string
	switch normalizeChatType(derefStr(resp.Data.ChatMode)) {
	case "p2p":
		ct = "p2p"
	case "group":
		ct = "group"
	default:
		return "", false
	}
	b.chatTypes.Store(chatID, ct)
	return ct, true
}

// derefStr safely dereferences a string pointer, returning empty string if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
