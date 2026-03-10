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
	"github.com/vaayne/anna/agent/runner"
	aitypes "github.com/vaayne/anna/ai/types"
)

const welcomeMessage = "Hi! I'm Anna -- your local AI assistant.\n\n" +
	"Commands:\n" +
	"/new -- Start a fresh session\n" +
	"/compact -- Compress conversation history\n" +
	"/model -- Switch between models\n\n" +
	"Just send me a message to get started."

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

	// Skip messages from the bot itself to prevent infinite loops in groups.
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

	// Dedup: Feishu retries events if the handler doesn't return quickly.
	if messageID != "" && b.markSeen(messageID) {
		logger().Debug("duplicate message ignored", "message_id", messageID)
		return nil
	}

	// Group mode check.
	if chatType == "group" && !b.shouldRespondInGroup(mentions) {
		return nil
	}

	// Parse message content.
	content := b.buildMessageContent(msg)
	if content == nil {
		return nil
	}

	// Extract text for command handling.
	text := parseTextContent(derefStr(msg.Content))
	if chatType == "group" {
		text = stripMentions(text, mentions)
	}

	ch := channelForChat(chatID)

	// Handle commands synchronously (they're fast).
	if text != "" {
		if handled := b.handleCommand(text, ch, func(reply string) {
			b.replyText(b.ctx, messageID, reply)
		}); handled {
			return nil
		}
	}

	// Process agent response asynchronously so the handler returns
	// immediately, preventing Feishu from retrying the event.
	go b.handleMessage(ch, chatID, messageID, content)
	return nil
}

// buildMessageContent constructs the message content from a Feishu message.
// Returns nil if the message has no usable content.
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
		// Rich text messages — pass raw JSON to the LLM for full context.
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
		return []aitypes.ContentBlock{
			aitypes.ImageContent{Data: encoded, MimeType: mime},
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
// Content format: {"text":"hello @_user_1 world"}
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
func (b *Bot) handleMessage(ch, chatID, messageID string, content runner.MessageContent) {
	sessionID, err := b.resolveSession(ch)
	if err != nil {
		logger().Error("resolve session failed", "channel", ch, "error", err)
		b.replyText(b.ctx, messageID, fmt.Sprintf("Session error: %v", err))
		return
	}

	logger().Debug("message received", "channel", ch)

	sentMsgID, response, images, streamErr := b.streamResponse(chatID, messageID, sessionID, content)

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

	logger().Debug("response sent", "channel", ch, "response_len", len(response), "images", len(images))
}

// handleCommand checks if text is a bot command and handles it.
// Returns true if the text was a command.
func (b *Bot) handleCommand(text, ch string, reply func(string)) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])

	switch cmd {
	case "/start", "/help":
		reply(welcomeMessage)
		return true

	case "/new":
		info, err := b.pool.RotateSession(ch)
		if err != nil {
			logger().Error("rotate session failed", "channel", ch, "error", err)
			reply(fmt.Sprintf("Error creating new session: %v", err))
			return true
		}
		logger().Info("new session created", "session_id", info.ID, "channel", ch)
		reply("New session started.")
		return true

	case "/compact":
		sessionID, err := b.resolveSession(ch)
		if err != nil {
			reply(fmt.Sprintf("No active session: %v", err))
			return true
		}
		summary, err := b.pool.CompactSession(b.ctx, sessionID)
		if err != nil {
			logger().Error("compact session failed", "session_id", sessionID, "error", err)
			reply(fmt.Sprintf("Compaction failed: %v", err))
			return true
		}
		logger().Info("session compacted", "session_id", sessionID, "summary_len", len(summary))
		reply("Session compacted.")
		return true

	case "/model":
		origCmd := strings.Fields(text)[0]
		args := strings.TrimSpace(strings.TrimPrefix(text, origCmd))
		b.handleModelCommand(args, ch, reply)
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
