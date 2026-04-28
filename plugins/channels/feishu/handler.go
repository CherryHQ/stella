package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	if chatType == "group" && !b.shouldRespondInGroup(chatID, nil) {
		return nil
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

	msg := b.incomingMsg(senderIDs, chatID, chatType, channel.TextContent(reactionText))
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

	// For file messages: send an immediate ack and resolve the per-user assets
	// directory before building content, so the downloaded file lands in the
	// user's persistent workspace rather than a throwaway temp directory.
	var assetsDir string
	if derefStr(msg.MessageType) == "file" {
		ackCtx, ackCancel := b.apiContext()
		b.replyInThread(ackCtx, messageID, rootID, "📎 Received file, processing...")
		ackCancel()

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

	// Prepend per-group system prompt to content if configured.
	if chatType == "group" {
		if sp := b.groupSystemPrompt(chatID); sp != "" {
			content = prependSystemPrompt(content, sp)
		}
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

// prependSystemPrompt adds a system prompt prefix to message content.
func prependSystemPrompt(content []ai.ContentBlock, prompt string) []ai.ContentBlock {
	prefix := ai.TextContent{Text: fmt.Sprintf("[System: %s]", prompt)}
	return append([]ai.ContentBlock{prefix}, content...)
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
				return channel.TextContent(fmt.Sprintf(
					"[File: %s — saved to %s]\nUse `kreuzberg extract %q` to read its content.",
					fileName, path, path,
				))
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
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.MessageResource.Get(apiCtx,
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

// downloadFile downloads a file from Feishu and saves it to assetsDir.
// The filename is prefixed with a Unix timestamp to avoid collisions.
func (b *Bot) downloadFile(messageID, fileKey, fileName, assetsDir string) (string, error) {
	apiCtx, cancel := b.apiContext()
	defer cancel()

	resp, err := b.client.Im.MessageResource.Get(apiCtx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type("file").
			Build())
	if err != nil {
		return "", fmt.Errorf("get resource: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("api error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return "", fmt.Errorf("empty file in response")
	}
	if closer, ok := resp.File.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	data, err := io.ReadAll(resp.File)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	name := fmt.Sprintf("%d_%s", time.Now().Unix(), filepath.Base(fileName))
	dst := filepath.Join(assetsDir, name)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	return dst, nil
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

// handleIncoming delegates to the coordinator via HandleIncoming.
// Shared commands are handled by the coordinator (including /abort);
// otherwise a chat stream is returned.
func (b *Bot) handleIncoming(msg channel.IncomingMessage, cmd, args, senderID, chatID, messageID, rootID string, replyFn func(string)) {
	// Use an operation-scoped context so in-flight work survives bot restarts
	// with bounded execution time. Keep it alive for the full streamed turn;
	// cancelling immediately after HandleIncoming returns would abort the agent
	// stream and release the per-session queue too early.
	ctx, cancel := b.operationContext()

	resp, handled, stream, err := b.handler.HandleIncoming(ctx, msg, cmd, args)
	if err != nil {
		cancel()
		logger().Error("chat failed", "sender_id", senderID, "error", err)
		replyCtx, cancel := b.apiContext()
		defer cancel()
		b.replyInThread(replyCtx, messageID, rootID, fmt.Sprintf("Session error: %v", err))
		return
	}
	if handled {
		defer cancel()
		replyFn(resp)
		return
	}
	defer cancel()

	logger().Debug("message received", "sender_id", senderID, "session", stream.SessionID, "root_id", rootID)

	sentMsgID, response, images, files, elapsed, streamErr := b.streamResponseInThread(stream.Events, chatID, messageID, rootID)

	if streamErr != nil {
		logger().Error("agent stream error", "session_id", stream.SessionID, "error", streamErr)
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
