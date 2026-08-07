package discord

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

const maxAttachmentBytes = 25 << 20

var errGuestAttachmentsUnsupported = errors.New("attachments are not supported in guest chat")

type channelContentBlock = ai.ContentBlock

func unwrapContent(v []channelContentBlock) []ai.ContentBlock { return v }

type groupMemberProvisioner interface {
	EnsurePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
}

func (b *Bot) handleMessage(ctx context.Context, m *discordgo.Message) error {
	deliveryCtx := context.WithoutCancel(ctx)
	if m.GuildID == "" && !b.cfg.AllowDM {
		logger().Debug("ignoring direct message because DMs are disabled", "channel_id", m.ChannelID)
		return nil
	}
	if !b.guildAllowed(m.GuildID) {
		logger().Debug("ignoring message from unconfigured guild", "guild_id", m.GuildID, "channel_id", m.ChannelID)
		return nil
	}
	if m.GuildID != "" && b.cfg.RequireMention && !b.mentioned(m) {
		logger().Debug("ignoring guild message without bot mention", "guild_id", m.GuildID, "channel_id", m.ChannelID)
		return nil
	}
	text := m.Content
	if m.GuildID != "" {
		text = b.stripBotMention(text)
	}
	content := make([]ai.ContentBlock, 0, 1+len(m.Attachments))
	if strings.TrimSpace(text) != "" {
		content = append(content, ai.TextContent{Text: text})
	}
	probe := b.incomingMessage(m, nil)
	var assetsRoot string
	if len(m.Attachments) > 0 {
		if resolver, ok := b.handler.(channel.UserRootResolver); ok {
			var err error
			assetsRoot, err = resolver.ResolveUserRoot(deliveryCtx, probe)
			if err != nil {
				// Resolve ownership before fetching untrusted content. In particular,
				// guest sessions have no workspace and must not trigger downloads.
				if errors.Is(err, agentaccess.ErrForbidden) {
					return errGuestAttachmentsUnsupported
				}
				return fmt.Errorf("attachments are unavailable for this Discord session: %w", err)
			}
		}
	}
	for _, attachment := range m.Attachments {
		content = append(content, b.attachmentContent(deliveryCtx, assetsRoot, attachment)...)
	}
	if len(content) == 0 {
		return nil
	}
	msg := b.incomingMessage(m, content)
	if msg.IsGroup {
		if err := b.ensureGroupMember(ctx, msg.ChatID); err != nil {
			return err
		}
	}
	cmd, args := channel.ParseSlashCommand(text)
	reply := func(text string) { _ = b.sendText(deliveryCtx, m.ChannelID, text, m.ID) }
	switch cmd {
	case "/model", "/agent":
		if admitter, ok := b.handler.(channel.LocalCommandAdmitter); ok {
			resp, handled, err := admitter.AdmitLocalCommand(ctx, msg)
			if err != nil {
				return err
			}
			if handled {
				reply(resp)
				return nil
			}
		}
	}
	switch cmd {
	case "/model":
		reply("The /model command is not available in Discord yet.")
		return nil
	case "/agent":
		if msg.IsGroup {
			reply("The /agent command is not available in Discord server channels.")
			return nil
		}
		channel.HandleAgentCommand(channel.AgentCommandHandler{
			Incoming:    msg,
			Args:        args,
			Reply:       reply,
			ListAgents:  b.handler.ListAgents,
			SwitchAgent: b.handler.SwitchAgent,
		})
		return nil
	}
	resp, handled, stream, err := b.handler.HandleIncoming(ctx, msg, cmd, args)
	if err != nil {
		return err
	}
	if handled {
		return b.sendText(deliveryCtx, m.ChannelID, resp, m.ID)
	}
	if stream == nil {
		return nil
	}
	_ = b.session.ChannelTyping(m.ChannelID)
	// Once accepted, consume the stream to completion. The managed runtime's
	// wrapped handler owns the operation lifetime; the gateway poll context may
	// be cancelled earlier during a graceful drain.
	textOut, images, files, streamErr := collectResponse(deliveryCtx, stream)
	if streamErr != nil {
		if textOut != "" {
			textOut += "\n\n"
		}
		textOut += "Agent error: " + streamErr.Error()
	}
	if strings.TrimSpace(textOut) == "" && len(images) == 0 && len(files) == 0 {
		textOut = "(empty response)"
	}
	if textOut != "" {
		if err := b.sendText(deliveryCtx, m.ChannelID, textOut, m.ID); err != nil {
			return err
		}
	}
	for _, image := range images {
		if err := b.sendImage(m.ChannelID, image); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := b.sendFile(m.ChannelID, file); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) ensureGroupMember(ctx context.Context, platformGroupID string) error {
	provisioner, ok := b.handler.(groupMemberProvisioner)
	if !ok {
		return nil
	}
	b.provisionMu.Lock()
	defer b.provisionMu.Unlock()
	if _, ok := b.provisionedGroups[platformGroupID]; ok {
		return nil
	}
	if err := provisioner.EnsurePlatformGroupMember(ctx, channel.PlatformDiscord, platformGroupID, b.Name()); err != nil {
		return fmt.Errorf("ensure discord group member: %w", err)
	}
	b.provisionedGroups[platformGroupID] = struct{}{}
	return nil
}

func collectResponse(ctx context.Context, stream *channel.ChatStream) (string, []channel.ImageEvent, []channel.FileEvent, error) {
	var text strings.Builder
	var images []channel.ImageEvent
	var files []channel.FileEvent
	var streamErr error
	for {
		select {
		case <-ctx.Done():
			return text.String(), images, files, ctx.Err()
		case evt, ok := <-stream.Events:
			if !ok {
				return text.String(), images, files, streamErr
			}
			if evt.Err != nil {
				streamErr = evt.Err
			}
			text.WriteString(evt.Text)
			if evt.Image != nil {
				images = append(images, *evt.Image)
			}
			if evt.File != nil {
				files = append(files, *evt.File)
			}
		}
	}
}

func (b *Bot) attachmentContent(ctx context.Context, assetsRoot string, a *discordgo.MessageAttachment) []ai.ContentBlock {
	if a == nil {
		return nil
	}
	name := filepath.Base(a.Filename)
	if name == "." || name == "" {
		name = a.ID
	}
	data, err := downloadAttachment(ctx, a.URL)
	if err != nil {
		logger().Warn("download attachment failed", "attachment_id", a.ID, "file_name", name, "error", err)
		return channel.TextContent(fmt.Sprintf("[Attachment: %s — download failed.]", name))
	}
	mime := http.DetectContentType(data)
	saver, ok := b.handler.(channel.AssetSaver)
	if ok && assetsRoot != "" {
		dir := agent.UserAssetsDir(assetsRoot)
		path, err := saver.SaveAsset(ctx, dir, name, data)
		if err != nil {
			logger().Warn("save attachment failed", "attachment_id", a.ID, "file_name", name, "error", err)
		} else {
			return channel.AttachmentReceivedContent(name, dir, path, data)
		}
	}
	if strings.HasPrefix(mime, "image/") {
		return channel.InlineImageFallback(name, mime, data)
	}
	return channel.AttachmentSaveFailureContent(name, data)
}

func allowedAttachmentURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "cdn.discordapp.com" || h == "media.discordapp.net"
}

func downloadAttachment(ctx context.Context, raw string) ([]byte, error) {
	if !allowedAttachmentURL(raw) {
		return nil, fmt.Errorf("disallowed discord attachment URL")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedAttachmentURL(req.URL.String()) {
			return fmt.Errorf("disallowed discord attachment redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("attachment HTTP status %s", resp.Status)
	}
	if resp.ContentLength > maxAttachmentBytes {
		return nil, fmt.Errorf("attachment exceeds 25 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("attachment exceeds 25 MiB")
	}
	return data, nil
}
