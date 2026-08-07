package discord

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

// maxAttachmentSize bounds an inbound attachment download.
const maxAttachmentSize = 50 << 20 // 50 MB

// onMessageCreate is the single Discord ingress handler. It applies the loop
// guard, the optional single-guild filter, and the B2 trigger gate before
// normalizing the message and forwarding it to the coordinator.
func (b *Bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Loop guard: Discord echoes the bot's own messages back over the gateway,
	// and we never respond to other bots.
	if m.Author == nil || m.Author.ID == b.botUserID() || m.Author.Bot {
		return
	}
	// Optional single-server filter.
	if b.cfg.GuildID != "" && m.GuildID != "" && m.GuildID != b.cfg.GuildID {
		return
	}

	// B2 trigger gate: gate guild messages, always pass DMs through.
	if m.GuildID != "" && !b.shouldRespond(m) {
		return
	}

	text := b.stripBotMention(m.Content)

	assetsDir := ""
	if len(m.Attachments) > 0 {
		assetsDir = b.resolveAssetsDir(m)
	}
	content := b.buildContent(m, text, assetsDir)
	if len(content) == 0 {
		return
	}

	var cmd, args string
	if fields := strings.Fields(text); len(fields) > 0 {
		cmd = fields[0]
		args = strings.TrimSpace(strings.TrimPrefix(text, cmd))
	}

	// /model and /agent need platform-specific UI, so the coordinator leaves them
	// to each channel (internal/channel/commands.go). Serve them as text lists.
	switch strings.ToLower(cmd) {
	case "/model":
		b.handleModelCommand(m, args)
		return
	case "/agent":
		b.handleAgentCommand(m, args)
		return
	}

	resp, handled, stream, err := b.handler.HandleIncoming(b.ctx, b.incomingMsg(m, content), cmd, args)
	if err != nil {
		b.sendPlain(m.ChannelID, fmt.Sprintf("Error: %v", err))
		return
	}
	if handled {
		b.sendPlain(m.ChannelID, resp)
		return
	}
	// Guard the events channel too: handleStream starts a typing goroutine and
	// ranges over Events, so a nil channel would leak the goroutine and block.
	if stream == nil || stream.Events == nil {
		return
	}
	b.handleStream(m, stream)
}

// shouldRespond applies the B2 trigger policy to a guild message: respond only
// when the bot is @-mentioned or the channel is whitelisted, unless MentionOnly
// is disabled (respond to everything in scope).
func (b *Bot) shouldRespond(m *discordgo.MessageCreate) bool {
	if !b.cfg.MentionOnly {
		return true
	}
	id := b.botUserID()
	for _, u := range m.Mentions {
		if u != nil && u.ID == id {
			return true
		}
	}
	return contains(b.cfg.RespondChannels, m.ChannelID)
}

// buildContent assembles the content blocks for a message: leading text plus any
// downloaded attachments.
func (b *Bot) buildContent(m *discordgo.MessageCreate, text, assetsDir string) []ai.ContentBlock {
	var content []ai.ContentBlock
	if text != "" {
		content = append(content, ai.TextContent{Text: text})
	}
	for _, att := range m.Attachments {
		if att == nil {
			continue
		}
		content = append(content, b.attachmentContent(att, assetsDir)...)
	}
	return content
}

// attachmentContent downloads a Discord attachment from its public CDN URL and
// returns the content blocks to route to the agent. A download or save failure
// never drops the turn: image bytes degrade via the shared inline fallback,
// other files get an explicit placeholder.
func (b *Bot) attachmentContent(att *discordgo.MessageAttachment, assetsDir string) []ai.ContentBlock {
	fileName := att.Filename
	if fileName == "" {
		fileName = att.ID
	}

	data, err := b.download(att.URL)
	if err != nil {
		logger().Warn("download attachment failed", "file_name", fileName, "error", err)
		return channel.TextContent(fmt.Sprintf("[Attachment %q could not be downloaded: %v]", fileName, err))
	}

	if assetsDir == "" {
		return channel.AttachmentSaveFailureContent(fileName, data)
	}
	savedPath, err := b.saveAsset(b.ctx, assetsDir, fileName, data)
	if err != nil {
		logger().Warn("save attachment failed", "file_name", fileName, "error", err)
		return channel.AttachmentSaveFailureContent(fileName, data)
	}
	logger().Debug("attachment received", "file_name", fileName, "size", len(data), "path", savedPath)
	return channel.AttachmentReceivedContent(fileName, assetsDir, savedPath, data)
}

// downloadTimeout bounds a single attachment fetch so a stalled CDN connection
// cannot pin the message handler indefinitely.
const downloadTimeout = 60 * time.Second

// download fetches an attachment from Discord's public CDN. No auth header is
// required.
func (b *Bot) download(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(b.ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentSize {
		return nil, fmt.Errorf("attachment too large (max %d MB)", maxAttachmentSize>>20)
	}
	return data, nil
}

// resolveAssetsDir resolves the per-user assets directory for the message's
// author, or "" when the handler cannot resolve a user root.
func (b *Bot) resolveAssetsDir(m *discordgo.MessageCreate) string {
	resolver, ok := b.handler.(channel.UserRootResolver)
	if !ok {
		return ""
	}
	userRoot, err := resolver.ResolveUserRoot(b.ctx, b.incomingMsg(m, nil))
	if err != nil {
		logger().Warn("resolve user root failed", "error", err)
		return ""
	}
	return agent.UserAssetsDir(userRoot)
}

// handleStream renders a ChatStream to the Discord channel.
func (b *Bot) handleStream(m *discordgo.MessageCreate, stream *channel.ChatStream) error {
	chatID := m.ChannelID
	logger().Debug("message received", "channel_id", chatID)

	typingCtx, stopTyping := context.WithCancel(b.ctx)
	go b.keepTyping(typingCtx, chatID)

	response, tracker, images, streamErr := b.streamEvents(chatID, stream.Events)

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

	b.sendFinalResponse(chatID, response, images)
	logger().Debug("response sent", "channel_id", chatID, "response_len", len(response))
	return nil
}
