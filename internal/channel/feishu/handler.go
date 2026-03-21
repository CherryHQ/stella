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
	"github.com/vaayne/anna/internal/feishutool"
)

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

	if !b.isAllowed(openID) {
		logger().Warn("unauthorized access", "open_id", openID)
		return nil
	}

	chatID := derefStr(msg.ChatId)
	chatType := derefStr(msg.ChatType)
	messageID := derefStr(msg.MessageId)
	mentions := msg.Mentions

	if messageID != "" && b.markSeen(messageID) {
		logger().Debug("duplicate message ignored", "message_id", messageID)
		return nil
	}

	if chatType == "group" && !b.shouldRespondInGroup(mentions) {
		return nil
	}

	content := b.buildMessageContent(msg)
	if content == nil {
		return nil
	}

	replyFn := func(reply string) { b.replyText(b.ctx, messageID, reply) }

	rc, err := b.resolve(openID, chatID, chatType)
	if err != nil {
		logger().Error("resolve failed", "open_id", openID, "error", err)
		replyFn(fmt.Sprintf("Error: %v", err))
		return nil
	}

	// Extract text for command handling.
	text := parseTextContent(derefStr(msg.Content))
	if chatType == "group" {
		text = stripMentions(text, mentions)
	}

	// Try link code before anything else.
	if b.authStore != nil && b.linkCodes != nil && text != "" {
		if resp, ok := channel.TryLinkCode(b.ctx, b.authStore, b.linkCodes, text, "feishu", openID, ""); ok {
			replyFn(resp)
			return nil
		}
	}

	// Handle /auth command before general command dispatch since it needs
	// messageID for sending interactive cards (not just text replies).
	if text != "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "/auth") {
		authArgs := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "/auth"))
		b.handleAuthCommand(openID, chatID, messageID, authArgs)
		return nil
	}

	if text != "" {
		if handled := b.handleCommand(rc, text, openID, replyFn); handled {
			return nil
		}
	}

	go b.handleMessage(rc, openID, chatID, messageID, content)
	return nil
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

	default:
		logger().Debug("unsupported message type", "type", msgType)
		return nil
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
func (b *Bot) handleMessage(rc *channel.ResolvedChat, openID, chatID, messageID string, content runner.MessageContent) {
	// Inject Feishu context so tools can access open_id, chat_id, message_id.
	ctx := feishutool.WithOpenID(b.ctx, openID)
	ctx = feishutool.WithChatID(ctx, chatID)
	ctx = feishutool.WithMessageID(ctx, messageID)

	events, sessionID, err := rc.Chat(ctx, content)
	if err != nil {
		logger().Error("chat failed", "open_id", openID, "error", err)
		b.replyText(b.ctx, messageID, fmt.Sprintf("Session error: %v", err))
		return
	}

	logger().Debug("message received", "open_id", openID, "session", sessionID)

	sentMsgID, response, images, streamErr := b.streamResponse(events, chatID, messageID)

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

	b.sendFinalResponse(chatID, messageID, sentMsgID, response)

	for _, img := range images {
		b.sendImage(chatID, messageID, img)
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

// derefStr safely dereferences a string pointer, returning empty string if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
