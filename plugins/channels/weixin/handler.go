package weixin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

// handleUpdates dispatches incoming messages from the getupdates response.
func (b *Bot) handleUpdates(msgs []WeixinMessage) {
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

		b.dispatchMessage(msg)
	}
}

// dispatchMessage processes all items in a message's item_list.
// A message may contain a mix of text, images, files, and videos.
// Text-only messages are handled as commands or chat. Messages with
// images (possibly with text captions) become multimodal content.
// File-only messages are downloaded and forwarded to the agent.
func (b *Bot) dispatchMessage(msg WeixinMessage) {
	texts, images := extractMessageContent(msg.ItemList)
	combinedText := strings.Join(texts, "\n")
	hasUnsupported := false
	for _, item := range msg.ItemList {
		switch item.Type {
		case ItemTypeText, ItemTypeImage, ItemTypeVoice, ItemTypeFile, ItemTypeVideo, ItemTypeUnsupported:
		default:
			hasUnsupported = true
		}
	}

	// Media-only message (file or voice with CDN, no images, no text) — download and forward.
	if len(images) == 0 && combinedText == "" {
		for _, item := range msg.ItemList {
			if item.Type == ItemTypeFile && item.FileItem != nil {
				b.handleFile(msg, item.FileItem)
				return
			}
			if item.Type == ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Media != nil {
				b.handleVoice(msg, item.VoiceItem)
				return
			}
		}
	}

	// Pure text message — handle as commands or chat.
	if len(images) == 0 {
		if combinedText != "" {
			b.handleText(msg, combinedText)
			return
		}
		if hasUnsupported {
			logger().Debug("unsupported message items only", "user_id", msg.FromUserID)
		}
		return
	}

	// Message has images — build multimodal content.
	b.handleImages(msg, images, combinedText)
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
func (b *Bot) handleText(msg WeixinMessage, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	reply := func(resp string) { b.sendReply(msg, resp) }
	incoming := b.incomingMsg(msg, channel.TextContent(text))

	// Handle plugin-local commands first.
	cmd, args := channel.ParseSlashCommand(text)
	switch cmd {
	case "/model":
		b.handleModelCommand(args, reply)
		return
	case "/agent":
		b.handleAgentCommand(msg, args, reply)
		return
	}

	// Delegate to coordinator (shared commands + chat streaming).
	b.handleIncoming(msg, incoming, cmd, args)
}

// extractMessageContent walks all items and returns text fragments and image items.
// Voice transcriptions, file names, video placeholders, and quoted messages are included as text.
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
		case ItemTypeFile:
			if item.FileItem != nil {
				name := item.FileItem.FileName
				if name == "" {
					name = "file"
				}
				texts = append(texts, fmt.Sprintf("[file: %s]", name))
			}
		case ItemTypeVideo:
			texts = append(texts, "[video]")
		case ItemTypeUnsupported:
			// type=0 is a protocol placeholder; nothing to extract.
		}
	}
	return
}

// handleImages processes one or more images with optional caption text.
func (b *Bot) handleImages(msg WeixinMessage, images []*ImageItem, caption string) {
	var content []ai.ContentBlock

	if caption != "" {
		content = append(content, ai.TextContent{Text: caption})
	}

	assetsDir := b.resolveAssetsDir(msg)
	for _, imageItem := range images {
		data, err := b.downloadImage(msg.FromUserID, imageItem)
		if err != nil {
			logger().Error("image processing failed", "user_id", msg.FromUserID, "error", err)
			continue
		}
		mimeType := http.DetectContentType(data)
		logger().Debug("image received", "user_id", msg.FromUserID, "size", len(data), "mime", mimeType)
		fileName := channel.ImageFileName("image", mimeType)
		if assetsDir != "" {
			savedPath, saveErr := b.saveAsset(b.ctx, assetsDir, fileName, data)
			if saveErr == nil {
				content = append(content, channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data)...)
				continue
			}
			logger().Warn("save inbound image failed", "user_id", msg.FromUserID, "error", saveErr)
		}
		// Persistence unavailable — degrade to inline within the ceiling; images
		// past the inline limit become an explicit text note instead.
		content = append(content, channel.InlineImageFallback(fileName, mimeType, data)...)
	}

	// Nothing decoded successfully.
	if len(content) == 0 || (len(content) == 1 && caption != "") {
		b.sendReply(msg, "Failed to process image(s).")
		return
	}

	incoming := b.incomingMsg(msg, content)
	b.handleIncoming(msg, incoming, "", "")
}

// resolveAssetsDir returns the user assets directory for msg, or "" if unavailable.
func (b *Bot) resolveAssetsDir(msg WeixinMessage) string {
	if resolver, ok := b.handler.(channel.UserRootResolver); ok {
		probeMsg := b.incomingMsg(msg, nil)
		if userRoot, err := resolver.ResolveUserRoot(b.ctx, probeMsg); err == nil {
			return agent.UserAssetsDir(userRoot)
		} else {
			logger().Warn("resolve user root failed", "user_id", msg.FromUserID, "error", err)
		}
	}
	return ""
}

// handleVoice handles a voice message item.
// Preference order:
//  1. Transcription text present: route as text message.
//  2. CDN media present: download, transcode (SILK→WAV stub), save to assets, route as file.
//  3. Neither: silently skip.
func (b *Bot) handleVoice(msg WeixinMessage, voiceItem *VoiceItem) {
	if voiceItem.Text != "" {
		b.handleText(msg, voiceItem.Text)
		return
	}

	if voiceItem.Media == nil || voiceItem.Media.EncryptQueryParam == "" {
		logger().Debug("voice item has no transcription and no CDN reference", "user_id", msg.FromUserID)
		return
	}

	assetsDir := b.resolveAssetsDir(msg)
	if assetsDir == "" {
		b.sendReply(msg, "[Voice message] (storage unavailable)")
		return
	}

	encrypted, err := DownloadFromCDN("", voiceItem.Media.FullURL, voiceItem.Media.EncryptQueryParam)
	if err != nil {
		logger().Error("voice cdn download failed", "user_id", msg.FromUserID, "error", err)
		b.sendReply(msg, "[Voice message] (download failed)")
		return
	}

	data := encrypted
	if voiceItem.Media.AESKey != "" {
		if key, keyErr := DecodeAESKey(voiceItem.Media.AESKey); keyErr == nil {
			if dec, decErr := DecryptAESECB(encrypted, key); decErr == nil {
				data = dec
			} else {
				logger().Warn("voice aes decrypt failed, using raw bytes", "user_id", msg.FromUserID, "error", decErr)
			}
		} else {
			logger().Warn("voice aes key decode failed", "user_id", msg.FromUserID, "error", keyErr)
		}
	}

	fileName := "voice.silk"
	fileData := data
	if wav := silkToWav(data); wav != nil {
		fileName = "voice.wav"
		fileData = wav
	}

	savedPath, err := b.saveAsset(b.ctx, assetsDir, fileName, fileData)
	if err != nil {
		logger().Error("save voice asset failed", "user_id", msg.FromUserID, "error", err)
		b.sendReply(msg, "[Voice message] (save failed)")
		return
	}

	logger().Debug("voice file received", "user_id", msg.FromUserID, "file_name", fileName, "size", len(fileData))
	incoming := b.incomingMsg(msg, channel.FileReceivedContent(fileName, assetsDir, savedPath))
	b.handleIncoming(msg, incoming, "", "")
}

// handleFile downloads a file from CDN, saves it to the user's assets directory,
// and forwards an Xberg extraction hint to the agent.
func (b *Bot) handleFile(msg WeixinMessage, fileItem *FileItem) {
	fileName := fileItem.FileName
	if fileName == "" {
		fileName = "file"
	}

	assetsDir := b.resolveAssetsDir(msg)
	if assetsDir == "" {
		b.sendReply(msg, fmt.Sprintf("[File: %s] (storage unavailable)", fileName))
		return
	}

	// Download from CDN.
	if fileItem.Media == nil || fileItem.Media.EncryptQueryParam == "" {
		b.sendReply(msg, fmt.Sprintf("[File: %s] (no CDN reference)", fileName))
		return
	}
	encrypted, err := DownloadFromCDN("", fileItem.Media.FullURL, fileItem.Media.EncryptQueryParam)
	if err != nil {
		logger().Error("cdn download failed for file", "user_id", msg.FromUserID, "error", err)
		b.sendReply(msg, fmt.Sprintf("[File: %s] (download failed)", fileName))
		return
	}

	// Decrypt if a key is present.
	data := encrypted
	if fileItem.Media.AESKey != "" {
		key, err := DecodeAESKey(fileItem.Media.AESKey)
		if err != nil {
			logger().Warn("aes key decode failed for file, using plaintext", "error", err)
		} else {
			if decrypted, err := DecryptAESECB(encrypted, key); err != nil {
				logger().Warn("aes decrypt failed for file, using raw bytes", "error", err)
			} else {
				data = decrypted
			}
		}
	}

	savedPath, err := b.saveAsset(b.ctx, assetsDir, fileName, data)
	if err != nil {
		// Persistence failed after a successful download — route a fallback to the
		// agent (image bytes inline within the ceiling, other files as a
		// placeholder) rather than dropping the turn.
		logger().Warn("save file asset failed", "user_id", msg.FromUserID, "error", err)
		incoming := b.incomingMsg(msg, channel.AttachmentSaveFailureContent(fileName, data))
		b.handleIncoming(msg, incoming, "", "")
		return
	}

	logger().Debug("file received", "user_id", msg.FromUserID, "file_name", fileName, "size", len(data))

	incoming := b.incomingMsg(msg, channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data))
	b.handleIncoming(msg, incoming, "", "")
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
func (b *Bot) handleIncoming(msg WeixinMessage, incoming channel.IncomingMessage, cmd, args string) {
	resp, handled, stream, err := b.handler.HandleIncoming(b.ctx, incoming, cmd, args)
	if err != nil {
		logger().Error("chat failed", "user_id", msg.FromUserID, "error", err)
		b.sendReply(msg, fmt.Sprintf("Session error: %v", err))
		return
	}
	if handled {
		b.sendReply(msg, resp)
		return
	}

	logger().Debug("message received", "user_id", msg.FromUserID, "session", stream.SessionID)

	// Start typing indicator.
	typingCtx, stopTyping := context.WithCancel(b.ctx)
	go b.keepTyping(typingCtx, msg)

	response, tracker, images, streamErr := b.streamEvents(msg, stream.Events)

	stopTyping()

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

	b.sendFinalResponse(msg, response, images)
	logger().Debug("response sent", "user_id", msg.FromUserID, "response_len", len(response))
}

// handleModelCommand processes /model with optional arguments.
func (b *Bot) handleModelCommand(args string, reply func(string)) {
	channel.HandleModelCommand(channel.ModelCommandHandler{
		Args:        args,
		Reply:       reply,
		ListModels:  b.handler.ListModels,
		SwitchModel: b.handler.SwitchModel,
		OnSwitched: func(selected channel.ModelOption) {
			logger().Info("model switched", "provider", selected.Provider, "model", selected.Model)
		},
	})
}

// handleAgentCommand processes /agent with optional arguments.
func (b *Bot) handleAgentCommand(msg WeixinMessage, args string, reply func(string)) {
	channel.HandleAgentCommand(channel.AgentCommandHandler{
		Incoming:    b.incomingMsg(msg, nil),
		Args:        args,
		Reply:       reply,
		ListAgents:  b.handler.ListAgents,
		SwitchAgent: b.handler.SwitchAgent,
		OnSwitched: func(agentID string) {
			logger().Info("agent switched", "agent_id", agentID)
		},
	})
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
