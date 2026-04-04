package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/channel"
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

	var openID string
	if data.UserId != nil && data.UserId.OpenId != nil {
		openID = *data.UserId.OpenId
	}
	if openID == "" {
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
	if chatType == "group" && !b.shouldRespondInGroup(chatID, nil) {
		return nil
	}

	reactionText := fmt.Sprintf("[User reacted with %s on message %s]", emojiType, messageID)

	rc, err := b.resolveWithThread(openID, chatID, chatType, rootID)
	if err != nil {
		logger().Error("resolve for reaction failed", "open_id", openID, "error", err)
		return nil
	}

	go b.handleMessage(rc, openID, chatID, messageID, rootID, reactionText)
	return nil
}

// onMessage handles incoming Feishu messages.
func (b *Bot) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}

	msg := event.Event.Message
	sender := event.Event.Sender

	if sender == nil || sender.SenderId == nil || sender.SenderId.OpenId == nil {
		return nil
	}

	openID := *sender.SenderId.OpenId

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

	if chatType == "group" && !b.shouldRespondInGroup(chatID, mentions) {
		return nil
	}

	// Extract text once for cancel detection, commands, and link codes.
	text := parseTextContent(derefStr(msg.Content))
	if chatType == "group" {
		text = stripMentions(text, mentions)
	}

	// Check for abort/cancel before processing the message.
	if isCancelText(text) {
		key := streamKey(chatID, rootID)
		if b.cancelStream(key) {
			b.replyInThread(b.ctx, messageID, rootID, "Cancelled.")
		}
		return nil
	}

	content := b.buildMessageContent(msg)
	if content == nil {
		return nil
	}

	replyFn := func(reply string) { b.replyInThread(b.ctx, messageID, rootID, reply) }

	rc, err := b.resolveWithThread(openID, chatID, chatType, rootID)
	if err != nil {
		logger().Error("resolve failed", "open_id", openID, "error", err)
		replyFn(fmt.Sprintf("Error: %v", err))
		return nil
	}

	// Try link code before anything else.
	if b.authStore != nil && b.linkCodes != nil && text != "" {
		if resp, ok := channel.TryLinkCode(b.ctx, b.authStore, b.linkCodes, text, channel.PlatformFeishu, openID, ""); ok {
			replyFn(resp)
			return nil
		}
	}

	if text != "" {
		if handled := b.handleCommand(rc, text, openID, replyFn); handled {
			return nil
		}
	}

	// Prepend per-group system prompt to content if configured.
	if chatType == "group" {
		if sp := b.groupSystemPrompt(chatID); sp != "" {
			content = prependSystemPrompt(content, sp)
		}
	}

	go b.handleMessage(rc, openID, chatID, messageID, rootID, content)
	return nil
}

// prependSystemPrompt adds a system prompt prefix to message content.
func prependSystemPrompt(content runner.MessageContent, prompt string) runner.MessageContent {
	switch c := content.(type) {
	case string:
		return fmt.Sprintf("[System: %s]\n\n%s", prompt, c)
	default:
		// For non-text content (images, etc.), wrap as-is.
		return content
	}
}

// buildMessageContent constructs the message content from a Feishu message.
func (b *Bot) buildMessageContent(msg *larkim.EventMessage) runner.MessageContent {
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
		return text

	case "post":
		if strings.TrimSpace(rawContent) == "" {
			return nil
		}
		return rawContent

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
		return parseAudioContent(rawContent)

	case "video", "media":
		return parseVideoContent(rawContent)

	case "file":
		return parseFileContent(rawContent)

	case "sticker":
		return parseStickerContent(rawContent)

	case "location":
		return parseLocationContent(rawContent)

	case "share_chat":
		return parseShareChatContent(rawContent)

	case "share_user":
		return parseShareUserContent(rawContent)

	case "merge_forward":
		return parseMergeForwardContent(rawContent)

	default:
		logger().Debug("unsupported message type", "type", msgType)
		return fmt.Sprintf("[Unsupported message type: %s]", msgType)
	}
}

// extractJSONField extracts a string field from a JSON object.
func extractJSONField(raw, field string) string {
	if raw == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// extractJSONInt extracts an integer field from a JSON object.
func extractJSONInt(raw, field string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return 0, false
	}
	v, ok := m[field]
	if !ok {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, false
	}
	return n, true
}

// parseAudioContent returns descriptive text for an audio message.
func parseAudioContent(raw string) string {
	duration, ok := extractJSONInt(raw, "duration")
	if ok && duration > 0 {
		return fmt.Sprintf("[Audio message, duration: %ds]", duration/1000)
	}
	return "[Audio message]"
}

// parseVideoContent returns descriptive text for a video message.
func parseVideoContent(raw string) string {
	duration, ok := extractJSONInt(raw, "duration")
	if ok && duration > 0 {
		return fmt.Sprintf("[Video message, duration: %ds]", duration/1000)
	}
	return "[Video message]"
}

// parseFileContent returns descriptive text for a file message.
func parseFileContent(raw string) string {
	name := extractJSONField(raw, "file_name")
	if name != "" {
		return fmt.Sprintf("[File: %s]", name)
	}
	return "[File]"
}

// parseStickerContent returns descriptive text for a sticker message.
func parseStickerContent(raw string) string {
	return "[Sticker]"
}

// parseLocationContent returns descriptive text for a location message.
func parseLocationContent(raw string) string {
	name := extractJSONField(raw, "name")
	lat := extractJSONField(raw, "latitude")
	lng := extractJSONField(raw, "longitude")
	if name != "" && lat != "" && lng != "" {
		return fmt.Sprintf("[Location: %s (%s, %s)]", name, lat, lng)
	}
	if name != "" {
		return fmt.Sprintf("[Location: %s]", name)
	}
	return "[Location]"
}

// parseShareChatContent returns descriptive text for a shared chat message.
func parseShareChatContent(raw string) string {
	chatID := extractJSONField(raw, "chat_id")
	if chatID != "" {
		return fmt.Sprintf("[Shared chat: %s]", chatID)
	}
	return "[Shared chat]"
}

// parseShareUserContent returns descriptive text for a shared user message.
func parseShareUserContent(raw string) string {
	userID := extractJSONField(raw, "user_id")
	if userID != "" {
		return fmt.Sprintf("[Shared user: %s]", userID)
	}
	return "[Shared user]"
}

// parseMergeForwardContent returns descriptive text for a merge-forwarded message.
func parseMergeForwardContent(raw string) string {
	// The merge_forward content is complex with nested messages.
	// We return a summary rather than recursively expanding.
	return "[Forwarded messages]"
}

// downloadImage downloads an image from Feishu using the MessageResource API.
func (b *Bot) downloadImage(messageID, imageKey string) ([]byte, string, error) {
	resp, err := b.client.Im.MessageResource.Get(b.ctx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(imageKey).
			Type("image").
			Build())
	if err != nil {
		return nil, "", fmt.Errorf("get resource: %w", err)
	}
	if !resp.Success() {
		return nil, "", fmt.Errorf("api error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return nil, "", fmt.Errorf("empty file in response")
	}
	if closer, ok := resp.File.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}

	mime := http.DetectContentType(data)
	return data, mime, nil
}

// parseTextContent extracts text from Feishu's JSON content format.
func parseTextContent(raw string) string {
	if raw == "" {
		return ""
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return raw
	}
	return content.Text
}

// handleMessage processes an incoming message by streaming the agent response.
func (b *Bot) handleMessage(rc *channel.ResolvedChat, openID, chatID, messageID, rootID string, content runner.MessageContent) {
	// Create cancellable context for abort support.
	ctx, cancel := context.WithCancel(b.ctx)
	key := streamKey(chatID, rootID)
	b.registerStream(key, cancel)
	defer b.unregisterStream(key)
	defer cancel()

	events, sessionID, err := rc.Chat(ctx, content)
	if err != nil {
		logger().Error("chat failed", "open_id", openID, "error", err)
		b.replyInThread(b.ctx, messageID, rootID, fmt.Sprintf("Session error: %v", err))
		return
	}

	logger().Debug("message received", "open_id", openID, "session", sessionID, "root_id", rootID)

	sentMsgID, response, images, elapsed, streamErr := b.streamResponseInThread(events, chatID, messageID, rootID)

	if streamErr != nil {
		logger().Error("agent stream error", "session_id", sessionID, "error", streamErr)
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

	b.sendFinalResponseInThread(chatID, messageID, rootID, sentMsgID, finalResponse)

	for _, img := range images {
		b.sendImageInThread(chatID, messageID, rootID, img)
	}

	logger().Debug("response sent", "open_id", openID, "response_len", len(response), "images", len(images))
}

// handleCommand checks if text is a bot command and handles it.
func (b *Bot) handleCommand(rc *channel.ResolvedChat, text, senderID string, reply func(string)) bool {
	if resp, ok := channel.HandleCommand(b.ctx, rc, text, senderID); ok {
		reply(resp)
		return true
	}

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])

	args := channel.ParseCommandArgs(text, fields[0])

	switch cmd {
	case "/auth":
		reply("The /auth command was removed. Feishu workspace OAuth is no longer supported. If you need Lark workspace access, install a lark-cli skill and run `lark-cli auth login --recommend`.")
		return true
	case "/model":
		b.handleModelCommand(rc, args, reply)
		return true
	case "/agent":
		channel.HandleAgentCommand(b.ctx, b.agentCmd, rc, args, reply)
		return true
	}

	return false
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
	resp, err := b.client.Im.Message.Get(b.ctx,
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
