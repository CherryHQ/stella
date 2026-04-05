package qq

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
)

// c2cMessageHandler returns a handler for private (C2C) messages.
func (b *Bot) c2cMessageHandler() event.C2CMessageEventHandler {
	return func(_ *dto.WSPayload, data *dto.WSC2CMessageData) error {
		msg := (*dto.Message)(data)
		authorID := msg.Author.ID
		text := strings.TrimSpace(msg.Content)

		content := b.buildMessageContent(msg)
		if content == nil {
			return nil
		}

		replyFn := func(reply string) { b.replyC2C(b.ctx, authorID, msg.ID, reply) }
		incoming := incomingMsg(authorID, "", content)

		if text != "" {
			if handled := b.handleCommand(incoming, text, replyFn); handled {
				return nil
			}
		}

		b.handleMessage(authorID, "", msg.ID, incoming, scopeC2C)
		return nil
	}
}

// groupATMessageHandler returns a handler for group @mention messages.
func (b *Bot) groupATMessageHandler() event.GroupATMessageEventHandler {
	return func(_ *dto.WSPayload, data *dto.WSGroupATMessageData) error {
		msg := (*dto.Message)(data)
		authorID := msg.Author.ID
		groupID := msg.GroupID

		if !b.shouldRespondInGroup() {
			return nil
		}

		content := b.buildMessageContent(msg)
		if content == nil {
			return nil
		}

		replyFn := func(reply string) { b.replyGroup(b.ctx, groupID, msg.ID, reply) }
		incoming := incomingMsg(authorID, groupID, content)

		text := strings.TrimSpace(msg.Content)
		if text != "" {
			if handled := b.handleCommand(incoming, text, replyFn); handled {
				return nil
			}
		}

		b.handleMessage(authorID, groupID, msg.ID, incoming, scopeGroup)
		return nil
	}
}

// messageScope indicates whether a message is from a C2C or group context.
type messageScope int

const (
	scopeC2C messageScope = iota
	scopeGroup
)

// buildMessageContent constructs the message content from a QQ message.
// Returns nil if the message has no usable content.
func (b *Bot) buildMessageContent(msg *dto.Message) any {
	text := strings.TrimSpace(msg.Content)
	images := extractImageAttachments(msg)

	if text == "" && len(images) == 0 {
		return nil
	}

	if len(images) == 0 {
		return text
	}

	var blocks []ai.ContentBlock
	if text != "" {
		blocks = append(blocks, ai.TextContent{Text: text})
	}
	for _, img := range images {
		data, mime, err := downloadImage(b.ctx, img.URL)
		if err != nil {
			logger().Warn("download image failed", "url", img.URL, "error", err)
			continue
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		blocks = append(blocks, ai.ImageContent{Data: encoded, MimeType: mime})
		logger().Debug("image received", "size", len(data), "mime", mime)
	}

	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

// extractImageAttachments returns image attachments from a QQ message.
func extractImageAttachments(msg *dto.Message) []*dto.MessageAttachment {
	var images []*dto.MessageAttachment
	for _, a := range msg.Attachments {
		if a.URL != "" && strings.HasPrefix(a.ContentType, "image/") {
			images = append(images, a)
		}
	}
	return images
}

const maxImageSize = 20 << 20 // 20 MB

// downloadImage fetches an image from a URL and returns the raw bytes and MIME type.
func downloadImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image: %w", err)
	}
	if len(data) > maxImageSize {
		return nil, "", fmt.Errorf("image too large (max %d bytes)", maxImageSize)
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}

	return data, mime, nil
}

// handleMessage processes an incoming message by streaming the agent response.
func (b *Bot) handleMessage(authorID, groupID, msgID string, incoming channel.IncomingMessage, scope messageScope) {
	replyTarget := authorID
	if groupID != "" {
		replyTarget = groupID
	}

	stream, err := b.handler.HandleMessage(b.ctx, incoming)
	if err != nil {
		logger().Error("chat failed", "author", authorID, "error", err)
		b.sendReply(replyTarget, msgID, fmt.Sprintf("Session error: %v", err), scope)
		return
	}

	logger().Debug("message received", "author", authorID, "session", stream.SessionID)

	response, images, streamErr := b.streamResponse(stream.Events, authorID, groupID, msgID, scope)

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

	b.sendFinalResponse(replyTarget, msgID, response, scope)

	for _, img := range images {
		b.sendImage(replyTarget, msgID, img, scope)
	}

	logger().Debug("response sent", "author", authorID, "response_len", len(response), "images", len(images))
}

// handleCommand checks if text is a bot command and handles it.
// Returns true if the text was a command.
func (b *Bot) handleCommand(incoming channel.IncomingMessage, text string, reply func(string)) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	args := channel.ParseCommandArgs(text, fields[0])

	// Try shared commands via handler (/start, /new, /compact, /whoami, /help, /link).
	if resp, ok := b.handler.HandleCommand(b.ctx, incoming, cmd, args); ok {
		reply(resp)
		return true
	}

	switch cmd {
	case "/model":
		b.handleModelCommand(args, reply)
		return true
	case "/agent":
		b.handleAgentCommand(incoming, args, reply)
		return true
	}

	return false
}

// shouldRespondInGroup checks whether the bot should respond based on group_mode.
func (b *Bot) shouldRespondInGroup() bool {
	return b.cfg.GroupMode != "disabled"
}

// sendReply is a convenience wrapper that dispatches to the correct scope.
func (b *Bot) sendReply(targetID, msgID, text string, scope messageScope) {
	switch scope {
	case scopeC2C:
		b.replyC2C(b.ctx, targetID, msgID, text)
	case scopeGroup:
		b.replyGroup(b.ctx, targetID, msgID, text)
	}
}

// replyC2C sends a text reply to a C2C (private) conversation.
func (b *Bot) replyC2C(ctx context.Context, userID, msgID, text string) {
	msg := dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   msgID,
	}
	if _, err := b.api.PostC2CMessage(ctx, userID, msg); err != nil {
		logger().Error("c2c reply failed", "user_id", userID, "error", err)
	}
}

// replyGroup sends a text reply to a group conversation.
func (b *Bot) replyGroup(ctx context.Context, groupID, msgID, text string) {
	msg := dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   msgID,
	}
	if _, err := b.api.PostGroupMessage(ctx, groupID, msg); err != nil {
		logger().Error("group reply failed", "group_id", groupID, "error", err)
	}
}
