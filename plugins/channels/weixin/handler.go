package weixin

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
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
// Files/videos are noted as placeholders.
func (b *Bot) dispatchMessage(msg WeixinMessage) {
	texts, images := extractMessageContent(msg.ItemList)
	combinedText := strings.Join(texts, "\n")
	hasUnsupported := false
	for _, item := range msg.ItemList {
		if item.Type != ItemTypeText && item.Type != ItemTypeImage &&
			item.Type != ItemTypeVoice && item.Type != ItemTypeFile && item.Type != ItemTypeVideo {
			hasUnsupported = true
			break
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
func (b *Bot) incomingMsg(msg WeixinMessage, content any) channel.IncomingMessage {
	return channel.IncomingMessage{
		Platform:   channel.PlatformWeixin,
		SenderID:   msg.FromUserID,
		SenderName: "", // no display name available from iLink
		ChatID:     msg.FromUserID,
		IsGroup:    false, // DM only for v1
		Content:    content,
	}
}

// handleText processes incoming text messages.
func (b *Bot) handleText(msg WeixinMessage, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	reply := func(resp string) { b.sendReply(msg, resp) }
	incoming := b.incomingMsg(msg, text)

	// Try shared commands via coordinator (/start, /new, /compact, /whoami, /link).
	fields := strings.Fields(text)
	if len(fields) > 0 {
		cmd := fields[0]
		args := strings.TrimSpace(strings.TrimPrefix(text, cmd))
		if resp, ok := b.handler.HandleCommand(b.ctx, incoming, cmd, args); ok {
			reply(resp)
			return
		}
	}

	// Parse command for channel-specific handling.
	if len(fields) > 0 {
		cmd := strings.ToLower(fields[0])
		args := channel.ParseCommandArgs(text, fields[0])

		switch cmd {
		case "/model":
			b.handleModelCommand(args, reply)
			return
		case "/agent":
			b.handleAgentCommand(msg, args, reply)
			return
		}
	}

	// Normal message — send to agent.
	b.handleMessage(msg, incoming)
}

// extractMessageContent walks all items and returns text fragments and image items.
// Voice transcriptions, file names, and video placeholders are included as text.
func extractMessageContent(items []MessageItem) (texts []string, images []*ImageItem) {
	for _, item := range items {
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

	for _, imageItem := range images {
		data, err := b.downloadImage(msg.FromUserID, imageItem)
		if err != nil {
			logger().Error("image processing failed", "user_id", msg.FromUserID, "error", err)
			continue
		}
		mimeType := http.DetectContentType(data)
		encoded := base64.StdEncoding.EncodeToString(data)
		content = append(content, ai.ImageContent{Data: encoded, MimeType: mimeType})
		logger().Debug("image received", "user_id", msg.FromUserID, "size", len(data), "mime", mimeType)
	}

	// Nothing decoded successfully.
	if len(content) == 0 || (len(content) == 1 && caption != "") {
		b.sendReply(msg, "Failed to process image(s).")
		return
	}

	incoming := b.incomingMsg(msg, content)
	b.handleMessage(msg, incoming)
}

// downloadImage fetches and decrypts a single image from CDN.
func (b *Bot) downloadImage(userID string, imageItem *ImageItem) ([]byte, error) {
	if imageItem.Media == nil || imageItem.Media.EncryptQueryParam == "" {
		return nil, fmt.Errorf("missing CDN media reference")
	}

	encrypted, err := DownloadFromCDN("", imageItem.Media.EncryptQueryParam)
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

// handleMessage is the common flow for text and multimodal messages.
func (b *Bot) handleMessage(msg WeixinMessage, incoming channel.IncomingMessage) {
	stream, err := b.handler.HandleMessage(b.ctx, incoming)
	if err != nil {
		logger().Error("chat failed", "user_id", msg.FromUserID, "error", err)
		b.sendReply(msg, fmt.Sprintf("Session error: %v", err))
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

	if tracker != nil && tracker.hasHistory() {
		response += tracker.renderFinal()
	}

	b.sendFinalResponse(msg, response, images)
	logger().Debug("response sent", "user_id", msg.FromUserID, "response_len", len(response))
}

// handleModelCommand processes /model with optional arguments.
// No args → list models; text with "/" → switch by name; text → filter.
func (b *Bot) handleModelCommand(args string, reply func(string)) {
	query := channel.ParseModelArgs(args)

	// If the query looks like "provider/model", try switching directly.
	if query != "" && strings.Contains(query, "/") {
		b.switchModelByName(query, reply)
		return
	}

	models := b.modelList(query)
	if len(models) == 0 {
		if query != "" {
			reply(fmt.Sprintf("No models matching %q.", query))
		} else {
			reply("No models configured.")
		}
		return
	}
	reply(formatModelList(models, query))
}

// modelList returns models optionally filtered by query.
func (b *Bot) modelList(query string) []channel.IndexedModel {
	models := b.handler.ListModels()
	if query == "" {
		return channel.IndexModels(models)
	}
	return channel.FilterModels(models, query)
}

// switchModelByName handles model switching by "provider/model" name.
func (b *Bot) switchModelByName(name string, reply func(string)) {
	name = strings.ToLower(strings.TrimSpace(name))
	models := b.handler.ListModels()
	var selected channel.ModelOption
	found := false
	for _, m := range models {
		if strings.ToLower(m.Provider+"/"+m.Model) == name {
			selected = m
			found = true
			break
		}
	}
	if !found {
		reply(fmt.Sprintf("Unknown model %q, use /model to list available models.", name))
		return
	}

	if err := b.handler.SwitchModel(selected.Provider, selected.Model); err != nil {
		reply(fmt.Sprintf("Error switching model: %v", err))
		return
	}
	logger().Info("model switched", "provider", selected.Provider, "model", selected.Model)
	reply(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
}

// handleAgentCommand processes /agent with optional arguments.
func (b *Bot) handleAgentCommand(msg WeixinMessage, args string, reply func(string)) {
	incoming := b.incomingMsg(msg, "")

	// Direct switch by slug.
	if args != "" {
		if err := b.handler.SwitchAgent(context.Background(), incoming, args); err != nil {
			reply(fmt.Sprintf("Error switching agent: %v", err))
			return
		}
		logger().Info("agent switched", "agent_id", args)
		reply(fmt.Sprintf("Switched to agent: %s", args))
		return
	}

	agents, currentAgentID, err := b.handler.ListAgents(context.Background(), incoming)
	if err != nil {
		reply(fmt.Sprintf("Error listing agents: %v", err))
		return
	}
	if len(agents) == 0 {
		reply("No agents available.")
		return
	}

	indexed := channel.IndexAgents(agents)
	reply(formatAgentList(indexed, currentAgentID))
}

// formatAgentList builds a text-based agent list.
func formatAgentList(agents []channel.IndexedAgent, currentID string) string {
	var sb strings.Builder
	sb.WriteString("Available agents:\n\n")
	for _, ag := range agents {
		prefix := "• "
		if ag.ID == currentID {
			prefix = "✅ "
		}
		fmt.Fprintf(&sb, "%s%s (%s)\n", prefix, ag.ID, ag.Name)
	}
	sb.WriteString("\nUse /agent <slug> to switch.")
	return sb.String()
}

// formatModelList builds a text-based model list.
func formatModelList(models []channel.IndexedModel, query string) string {
	var sb strings.Builder
	sb.WriteString("Available models")
	if query != "" {
		fmt.Fprintf(&sb, " (filter: %q)", query)
	}
	sb.WriteString(":\n\n")
	for _, m := range models {
		fmt.Fprintf(&sb, "• %s/%s\n", m.Provider, m.Model)
	}
	sb.WriteString("\nUse /model <provider/model> to switch.")
	return sb.String()
}

// sendReply sends a text reply to the message sender using the cached context_token.
func (b *Bot) sendReply(msg WeixinMessage, text string) {
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

	if err := b.client.SendMessage(reply, ""); err != nil {
		logger().Error("send reply failed", "user_id", msg.FromUserID, "error", err)
	}
}
