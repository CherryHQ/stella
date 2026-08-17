package weixin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

// handleUpdates dispatches incoming messages from the getupdates response.
func (b *Bot) handleUpdates(msgs []WeixinMessage) error {
	for i := range msgs {
		msg := msgs[i]

		// Skip bot echoes — only process user messages.
		if msg.MessageType != MessageTypeUser {
			continue
		}

		// Skip partial/generating states — only process finished messages.
		if msg.MessageState != MessageStateFinish {
			continue
		}

		// Cache context_token for this user.
		if msg.ContextToken != "" {
			b.contextTokens.Store(msg.FromUserID, msg.ContextToken)
		}

		if len(msg.ItemList) == 0 {
			continue
		}

		if err := b.dispatchMessage(msg); err != nil {
			return err
		}
	}
	return nil
}

// dispatchMessage processes all items in a message's item_list.
// A message may contain a mix of text, images, files, and videos.
// Text-only messages are handled as commands or chat. Messages with
// images (possibly with text captions) become multimodal content.
// File-only messages are downloaded and forwarded to the agent.
func (b *Bot) dispatchMessage(msg WeixinMessage) error {
	texts, images := extractMessageContent(msg.ItemList)
	combinedText := strings.Join(texts, "\n")
	hasUnsupported := false
	hasAttachments := len(images) > 0
	for _, item := range msg.ItemList {
		switch item.Type {
		case ItemTypeText, ItemTypeImage, ItemTypeVoice, ItemTypeFile, ItemTypeVideo, ItemTypeUnsupported:
			if item.Type == ItemTypeFile || item.Type == ItemTypeVideo || (item.Type == ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Media != nil) {
				hasAttachments = true
			}
		default:
			hasUnsupported = true
		}
	}
	if !hasAttachments {
		if combinedText != "" {
			return b.handleText(msg, combinedText)
		}
		if hasUnsupported {
			logger().Debug("unsupported message items only", "user_id", msg.FromUserID)
		}
		return nil
	}
	content, err := b.buildAttachmentContent(msg, combinedText)
	if err != nil {
		return err
	}
	return b.handleIncoming(msg, b.incomingMsg(msg, content), "", "")
}

// incomingMsg builds a channel.IncomingMessage from a weixin message context.
func (b *Bot) incomingMsg(msg WeixinMessage, content []ai.ContentBlock) channel.IncomingMessage {
	im := channel.IncomingMessage{
		Platform:   channel.PlatformWeixin,
		ChannelID:  b.Name(),
		SenderID:   msg.FromUserID,
		SenderName: "", // no display name available from iLink
		ChatID:     msg.FromUserID,
		IsGroup:    false, // DM only for v1
		Content:    content,
	}
	if msg.MessageID != 0 {
		im.MessageID = strconv.FormatInt(msg.MessageID, 10)
	}
	if msg.CreateTimeMS != 0 {
		im.Timestamp = time.UnixMilli(msg.CreateTimeMS).UTC()
	}
	return im
}

// handleText processes incoming text messages.
func (b *Bot) handleText(msg WeixinMessage, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	incoming := b.incomingMsg(msg, channel.TextContent(text))

	cmd, args := channel.ParseSlashCommand(text)
	// Delegate to coordinator (shared commands + chat streaming).
	return b.handleIncoming(msg, incoming, cmd, args)
}

// extractMessageContent walks all items and returns genuine text fragments and
// image items. File/video attachment placeholders are deliberately excluded:
// accepting one as text would acknowledge a delivery whose expiring bytes were
// never made immutable.
func extractMessageContent(items []MessageItem) (texts []string, images []*ImageItem) {
	for _, item := range items {
		// Quoted/referenced message — prepend as context before the item's own content.
		if item.RefMsg != nil {
			ref := item.RefMsg
			if ref.Title != "" {
				texts = append(texts, fmt.Sprintf("[引用: %s]", ref.Title))
			} else if ref.MessageItem != nil {
				refTexts, _ := extractMessageContent([]MessageItem{*ref.MessageItem})
				for _, t := range refTexts {
					texts = append(texts, fmt.Sprintf("[引用: %s]", t))
				}
			}
		}

		switch item.Type {
		case ItemTypeText:
			if item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
				texts = append(texts, strings.TrimSpace(item.TextItem.Text))
			}
		case ItemTypeImage:
			if item.ImageItem != nil {
				images = append(images, item.ImageItem)
			}
		case ItemTypeVoice:
			if item.VoiceItem != nil && item.VoiceItem.Text != "" {
				texts = append(texts, item.VoiceItem.Text)
			}
		case ItemTypeFile, ItemTypeVideo:
			// Bytes are materialized by buildAttachmentContent.
		case ItemTypeUnsupported:
			// type=0 is a protocol placeholder; nothing to extract.
		}
	}
	return
}

func (b *Bot) buildAttachmentContent(msg WeixinMessage, caption string) ([]ai.ContentBlock, error) {
	var content []ai.ContentBlock
	if caption != "" {
		content = append(content, ai.TextContent{Text: caption})
	}
	assetMsg := b.resolveAssetsDir(msg)
	if assetMsg.Platform == "" {
		return nil, fmt.Errorf("immutable attachment storage admission unavailable")
	}
	attachments := 0
	for _, item := range msg.ItemList {
		var fileName string
		var data []byte
		var err error
		switch item.Type {
		case ItemTypeImage:
			if item.ImageItem == nil {
				return nil, fmt.Errorf("image attachment metadata is missing")
			}
			data, err = b.downloadImage(msg.FromUserID, item.ImageItem)
			if err == nil {
				fileName = channel.ImageFileName("image", http.DetectContentType(data))
			}
		case ItemTypeFile:
			if item.FileItem == nil {
				return nil, fmt.Errorf("file attachment metadata is missing")
			}
			fileName = item.FileItem.FileName
			if fileName == "" {
				fileName = "file"
			}
			data, err = downloadWeixinMedia(item.FileItem.Media)
		case ItemTypeVoice:
			if item.VoiceItem == nil || item.VoiceItem.Media == nil {
				continue
			}
			fileName = "voice.silk"
			data, err = downloadWeixinMedia(item.VoiceItem.Media)
			if err == nil {
				if wav := silkToWav(data); wav != nil {
					fileName, data = "voice.wav", wav
				}
			}
		case ItemTypeVideo:
			if item.VideoItem == nil {
				return nil, fmt.Errorf("video attachment metadata is missing")
			}
			fileName = "video.mp4"
			data, err = downloadWeixinMedia(item.VideoItem.Media)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", fileName, err)
		}
		savedPath, err := b.saveAsset(b.ctx, assetMsg, fileName, data)
		if err != nil {
			return nil, fmt.Errorf("persist %s before admission: %w", fileName, err)
		}
		content = append(content, channel.AttachmentReceivedContent(fileName, savedPath, data)...)
		attachments++
	}
	if attachments == 0 {
		return nil, fmt.Errorf("attachment delivery contains no downloadable media")
	}
	return content, nil
}

func downloadWeixinMedia(media *CDNMedia) ([]byte, error) {
	if media == nil || media.EncryptQueryParam == "" {
		return nil, fmt.Errorf("missing CDN media reference")
	}
	data, err := DownloadFromCDN("", media.FullURL, media.EncryptQueryParam)
	if err != nil {
		return nil, err
	}
	if media.AESKey == "" {
		return data, nil
	}
	key, err := DecodeAESKey(media.AESKey)
	if err != nil {
		return nil, fmt.Errorf("decode AES key: %w", err)
	}
	data, err = DecryptAESECB(data, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt media: %w", err)
	}
	return data, nil
}

// resolveAssetsDir returns the user assets directory for msg, or "" if unavailable.
func (b *Bot) resolveAssetsDir(msg WeixinMessage) channel.IncomingMessage {
	if resolver, ok := b.handler.(channel.AssetSaveAdmitter); ok {
		probeMsg := b.incomingMsg(msg, nil)
		if err := resolver.AdmitAssetSave(b.ctx, probeMsg); err == nil {
			return probeMsg
		} else {
			logger().Warn("resolve user root failed", "user_id", msg.FromUserID, "error", err)
		}
	}
	return channel.IncomingMessage{}
}

// downloadImage fetches and decrypts a single image from CDN.
func (b *Bot) downloadImage(userID string, imageItem *ImageItem) ([]byte, error) {
	if imageItem.Media == nil || imageItem.Media.EncryptQueryParam == "" {
		return nil, fmt.Errorf("missing CDN media reference")
	}

	encrypted, err := DownloadFromCDN("", imageItem.Media.FullURL, imageItem.Media.EncryptQueryParam)
	if err != nil {
		return nil, fmt.Errorf("cdn download: %w", err)
	}

	key, keyErr := ResolveImageKey(imageItem)
	if keyErr != nil {
		logger().Warn("no AES key for image, using plaintext", "user_id", userID, "error", keyErr)
		return encrypted, nil
	}

	data, err := DecryptAESECB(encrypted, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return data, nil
}

// handleIncoming delegates to the coordinator via HandleIncoming.
func (b *Bot) handleIncoming(msg WeixinMessage, incoming channel.IncomingMessage, cmd, args string) error {
	if ai.HasAttachment(incoming.Content) {
		admitter, ok := b.handler.(channel.AttachmentAdmitter)
		if !ok {
			return fmt.Errorf("durable attachment admission unavailable")
		}
		if err := admitter.AdmitAttachments(b.ctx, incoming); err != nil {
			logger().Warn("durable attachment admission failed", "user_id", msg.FromUserID, "error", err)
			return fmt.Errorf("durably admit attachment: %w", err)
		}
	}
	resp, handled, stream, err := b.handler.HandleIncoming(b.ctx, incoming, cmd, args)
	if err != nil {
		logger().Error("chat failed", "user_id", msg.FromUserID, "error", err)
		b.sendReply(msg, fmt.Sprintf("Session error: %v", err))
		return nil
	}
	if handled {
		b.sendReply(msg, resp)
		return nil
	}
	if stream == nil {
		return nil
	}

	logger().Debug("message received", "user_id", msg.FromUserID, "session", stream.SessionID)

	response, tracker, images, streamErr := b.streamEvents(msg, stream.Events)

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

	if tracker != nil && tracker.HasHistory() {
		response += tracker.RenderFinal()
	}

	if err := b.sendFinalResponse(b.ctx, stream, msg, response, images); err != nil {
		logger().Error("send final response failed", "user_id", msg.FromUserID, "error", err)
		return nil
	}
	logger().Debug("response sent", "user_id", msg.FromUserID, "response_len", len(response))
	return nil
}

// sendReply sends a text reply to the message sender using the cached context_token.
func (b *Bot) sendReply(msg WeixinMessage, text string) {
	if err := b.guard.AssertActive(); err != nil {
		logger().Warn("sendReply skipped: session paused", "user_id", msg.FromUserID, "error", err)
		return
	}
	// Load context_token for this user.
	contextToken := ""
	if v, ok := b.contextTokens.Load(msg.FromUserID); ok {
		contextToken, _ = v.(string)
	}

	reply := WeixinMessage{
		ToUserID:     msg.FromUserID,
		ClientID:     RandomClientID("reply"),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		ItemList: []MessageItem{
			{
				Type:     ItemTypeText,
				TextItem: &TextItem{Text: text},
			},
		},
	}

	if err := b.client.SendMessage(reply); err != nil {
		logger().Error("send reply failed", "user_id", msg.FromUserID, "error", err)
	}
}
