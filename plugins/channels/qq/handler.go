package qq

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/httpclient"
)

// c2cMessageHandler returns a handler for private (C2C) messages.
func (b *Bot) c2cMessageHandler() event.C2CMessageEventHandler {
	return func(_ *dto.WSPayload, data *dto.WSC2CMessageData) error {
		msg := (*dto.Message)(data)
		authorID := msg.Author.ID

		assetsDir := b.resolveAssetsDir(b.incomingMsg(authorID, "", nil), msg)
		content := b.buildMessageContent(msg, assetsDir, false)
		if content == nil {
			return nil
		}

		replyFn := func(reply string) { b.replyC2C(b.ctx, authorID, msg.ID, reply) }
		incoming := b.incomingMsg(authorID, "", content)
		fillQQMeta(&incoming, msg)

		text := strings.TrimSpace(msg.Content)
		if handled := b.handleLocalCommand(incoming, text, replyFn); handled {
			return nil
		}

		cmd, args := channel.ParseSlashCommand(text)
		b.handleIncoming(authorID, "", msg.ID, incoming, cmd, args, scopeC2C)
		return nil
	}
}

// groupATMessageHandler returns a handler for group @mention messages.
func (b *Bot) groupATMessageHandler() event.GroupATMessageEventHandler {
	return func(_ *dto.WSPayload, data *dto.WSGroupATMessageData) error {
		msg := (*dto.Message)(data)
		authorID := msg.Author.ID
		groupID := msg.GroupID

		assetsDir := b.resolveAssetsDir(b.incomingMsg(authorID, groupID, nil), msg)
		content := b.buildMessageContent(msg, assetsDir, true)
		if content == nil {
			return nil
		}

		replyFn := func(reply string) { b.replyGroup(b.ctx, groupID, msg.ID, reply) }
		incoming := b.incomingMsg(authorID, groupID, content)
		fillQQMeta(&incoming, msg)

		text := strings.TrimSpace(msg.Content)
		if handled := b.handleLocalCommand(incoming, text, replyFn); handled {
			return nil
		}

		cmd, args := channel.ParseSlashCommand(text)
		b.handleIncoming(authorID, groupID, msg.ID, incoming, cmd, args, scopeGroup)
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
// assetsDir is the resolved per-user assets directory; pass "" when unavailable
// (file attachments will be represented as placeholder text instead).
// Returns nil if the message has no usable content.
func (b *Bot) buildMessageContent(msg *dto.Message, assetsDir string, deferredGroup bool) []ai.ContentBlock {
	text := strings.TrimSpace(msg.Content)
	images := extractImageAttachments(msg)
	files := extractFileAttachments(msg)

	if text == "" && len(images) == 0 && len(files) == 0 {
		return nil
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
		logger().Debug("image received", "size", len(data), "mime", mime)
		fileName := img.FileName
		if fileName == "" {
			fileName = channel.ImageFileName("image", mime)
		}
		if assetsDir != "" {
			savedPath, saveErr := b.saveAsset(b.ctx, assetsDir, fileName, data)
			if saveErr == nil {
				blocks = append(blocks, channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data, deferredGroup)...)
				continue
			}
			logger().Warn("save inbound image failed", "error", saveErr)
		}
		// Persistence unavailable — degrade to inline within the ceiling; images
		// past the inline limit become an explicit text note instead.
		blocks = append(blocks, channel.InlineImageFallback(fileName, mime, data, deferredGroup)...)
	}
	for _, f := range files {
		fileName := f.FileName
		if fileName == "" {
			fileName = "file"
		}
		if assetsDir != "" {
			data, _, err := downloadImage(b.ctx, f.URL) // reuse HTTP downloader
			if err != nil {
				logger().Warn("download file attachment failed", "url", f.URL, "error", err)
				blocks = append(blocks, ai.TextContent{Text: fmt.Sprintf("[File: %s] (download failed)", fileName)})
				continue
			}
			savedPath, err := b.saveAsset(b.ctx, assetsDir, fileName, data)
			if err != nil {
				logger().Warn("save file attachment failed", "error", err)
				blocks = append(blocks, channel.AttachmentSaveFailureContent(fileName, data, deferredGroup)...)
				continue
			}
			logger().Debug("file attachment received", "file_name", fileName, "size", len(data))
			blocks = append(blocks, channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data, deferredGroup)...)
		} else {
			blocks = append(blocks, ai.TextContent{Text: fmt.Sprintf("[File: %s]", fileName)})
		}
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

// extractFileAttachments returns non-image, non-video, non-voice attachments.
func extractFileAttachments(msg *dto.Message) []*dto.MessageAttachment {
	var files []*dto.MessageAttachment
	for _, a := range msg.Attachments {
		if a.URL == "" {
			continue
		}
		ct := a.ContentType
		if strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "voice") {
			continue
		}
		files = append(files, a)
	}
	return files
}

// resolveAssetsDir returns the per-user assets directory if the handler supports
// UserRootResolver and the message contains image or file attachments to persist.
// Returns "" otherwise.
func (b *Bot) resolveAssetsDir(probeMsg channel.IncomingMessage, msg *dto.Message) string {
	if len(extractImageAttachments(msg)) == 0 && len(extractFileAttachments(msg)) == 0 {
		return ""
	}
	resolver, ok := b.handler.(channel.UserRootResolver)
	if !ok {
		return ""
	}
	userRoot, err := resolver.ResolveUserRoot(b.ctx, probeMsg)
	if err != nil {
		logger().Warn("resolve user root failed for file attachment", "error", err)
		return ""
	}
	return agent.UserAssetsDir(userRoot)
}

const maxImageSize = 20 << 20 // 20 MB

// downloadImage fetches an image from a URL and returns the raw bytes and MIME type.
func downloadImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	resp, err := httpclient.New().R().
		SetContext(ctx).
		Get(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("fetch image: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode())
	}

	data := resp.Body()
	if len(data) > maxImageSize {
		return nil, "", fmt.Errorf("image too large (max %d bytes)", maxImageSize)
	}

	mimeType := resp.Header().Get("Content-Type")
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	return data, mimeType, nil
}

// handleIncoming delegates shared command handling, including /abort,
// to the coordinator via HandleIncoming. If the message is not handled as a
// command, the coordinator returns a chat stream for this channel to render.
func (b *Bot) handleIncoming(authorID, groupID, msgID string, incoming channel.IncomingMessage, cmd, args string, scope messageScope) {
	replyTarget := authorID
	if groupID != "" {
		replyTarget = groupID
	}

	resp, handled, stream, err := b.handler.HandleIncoming(b.ctx, incoming, cmd, args)
	if err != nil {
		logger().Error("chat failed", "author", authorID, "error", err)
		b.sendReply(replyTarget, msgID, fmt.Sprintf("Session error: %v", err), scope)
		return
	}
	if handled {
		b.sendReply(replyTarget, msgID, resp, scope)
		return
	}
	if stream == nil {
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

// handleLocalCommand handles plugin-local commands (/model, /agent, /help, /whoami).
// Returns true if the text was handled.
func (b *Bot) handleLocalCommand(incoming channel.IncomingMessage, text string, reply func(string)) bool {
	cmd, args := channel.ParseSlashCommand(text)
	if cmd == "" {
		return false
	}

	switch cmd {
	case "/model":
		b.handleModelCommand(args, reply)
		return true
	case "/agent":
		b.handleAgentCommand(incoming, args, reply)
		return true
	case "/help":
		reply(channel.WelcomeMessage)
		return true
	case "/whoami":
		reply(fmt.Sprintf("Your sender ID: %s", incoming.SenderID))
		return true
	}

	return false
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
