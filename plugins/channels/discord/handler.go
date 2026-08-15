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

type threadGroupMemberProvisioner interface {
	EnsurePlatformThreadGroupMember(ctx context.Context, platform, platformGroupID, platformThreadID, legacyPlatformGroupID, channelID string) error
}

type groupHistoryImporter interface {
	ImportGroupHistory(ctx context.Context, messages []channel.IncomingMessage) error
}

func (b *Bot) handleMessage(ctx context.Context, m *discordgo.Message) error {
	deliveryCtx := context.WithoutCancel(ctx)
	if m.GuildID == "" && !b.cfg.AllowDM {
		logger().Debug("ignoring direct message because DMs are disabled", "channel_id", m.ChannelID)
		return nil
	}
	route := messageRoute{chatID: m.ChannelID}
	if m.GuildID != "" {
		allowed, resolvedRoute, err := b.groupAccessAllowed(deliveryCtx, m)
		if err != nil {
			return err
		}
		if !allowed {
			logger().Debug("ignoring message from unconfigured guild, channel, user, or role", "guild_id", m.GuildID, "channel_id", m.ChannelID)
			return nil
		}
		route = resolvedRoute
		if b.cfg.RequireMention && !b.addressed(m) {
			logger().Debug("ignoring guild message without bot mention", "guild_id", m.GuildID, "channel_id", m.ChannelID)
			return nil
		}
	}
	// Acknowledge immediately; attachment downloads, thread-history reads, and
	// durable group dispatch can all take longer than Discord's typing TTL.
	stopTyping := b.startTypingHeartbeat(m.ChannelID)
	defer stopTyping()
	text := m.Content
	if m.GuildID != "" {
		text = b.stripBotMention(text)
	}
	content := make([]ai.ContentBlock, 0, 1+len(m.Attachments))
	if strings.TrimSpace(text) != "" {
		content = append(content, ai.TextContent{Text: text})
	}
	probe := b.incomingMessage(m, nil, route.chatID, route.threadID)
	var assetMsg channel.IncomingMessage
	if len(m.Attachments) > 0 {
		resolver, ok := b.handler.(channel.AssetSaveAdmitter)
		if !ok {
			return errors.New("attachment storage admission unavailable")
		}
		if err := resolver.AdmitAssetSave(deliveryCtx, probe); err != nil {
			// Resolve and authorize ownership before fetching untrusted content.
			if errors.Is(err, agentaccess.ErrForbidden) {
				return errGuestAttachmentsUnsupported
			}
			return fmt.Errorf("admit attachment storage: %w", err)
		}
		assetMsg = probe
	}
	for _, attachment := range m.Attachments {
		content = append(content, b.attachmentContent(deliveryCtx, assetMsg, attachment)...)
	}
	history, err := b.loadThreadHistory(deliveryCtx, m, route)
	if err != nil {
		return err
	}
	if len(content) == 0 && len(history) > 0 {
		content = append(content, ai.TextContent{Text: "[Mentioned Stella without additional text.]"})
	}
	if len(content) == 0 {
		return nil
	}
	msg := b.incomingMessage(m, content, route.chatID, route.threadID)
	if msg.IsGroup {
		if err := b.ensureGroupMember(deliveryCtx, msg.ChatID, msg.ThreadID); err != nil {
			return err
		}
		if len(history) > 0 {
			importer, ok := b.handler.(groupHistoryImporter)
			if !ok {
				return errors.New("group history import unavailable")
			}
			if err := importer.ImportGroupHistory(deliveryCtx, history); err != nil {
				return fmt.Errorf("import Discord thread history: %w", err)
			}
		}
	}
	cmd, args := channel.ParseSlashCommand(text)
	resp, handled, stream, err := b.handler.HandleIncoming(deliveryCtx, msg, cmd, args)
	if err != nil {
		return err
	}
	if handled {
		return b.sendText(deliveryCtx, m.ChannelID, resp, m.ID)
	}
	if stream == nil {
		return nil
	}
	// Once accepted, consume the stream to completion. The managed runtime's
	// wrapped handler owns the operation lifetime; the gateway poll context may
	// be cancelled earlier during a graceful drain.
	return b.deliverStream(deliveryCtx, m.ChannelID, m.ID, stream)
}

func (b *Bot) ensureGroupMember(ctx context.Context, platformGroupID, platformThreadID string) error {
	cacheKey := platformGroupID + "\x00" + platformThreadID
	b.provisionMu.Lock()
	_, provisioned := b.provisionedGroups[cacheKey]
	b.provisionMu.Unlock()
	if provisioned {
		return nil
	}
	if platformThreadID != "" {
		provisioner, ok := b.handler.(threadGroupMemberProvisioner)
		if !ok {
			return errors.New("thread group member provisioning unavailable")
		}
		// Before thread-scoped routing existed, Discord treated the thread
		// channel itself as the group, with no separate thread ID. Pass that
		// legacy identity so a pre-existing group there is adopted instead of
		// silently starting the thread over with empty history.
		if err := provisioner.EnsurePlatformThreadGroupMember(ctx, channel.PlatformDiscord, platformGroupID, platformThreadID, platformThreadID, b.Name()); err != nil {
			return fmt.Errorf("ensure discord thread group member: %w", err)
		}
	} else {
		provisioner, ok := b.handler.(groupMemberProvisioner)
		if !ok {
			return nil
		}
		if err := provisioner.EnsurePlatformGroupMember(ctx, channel.PlatformDiscord, platformGroupID, b.Name()); err != nil {
			return fmt.Errorf("ensure discord group member: %w", err)
		}
	}
	b.provisionMu.Lock()
	if len(b.provisionedGroups) >= provisionedGroupCacheLimit {
		// Bounded cache only; provisioning is idempotent, so clearing trades a
		// future DB check for bounded memory when a forum creates many threads.
		clear(b.provisionedGroups)
	}
	b.provisionedGroups[cacheKey] = struct{}{}
	b.provisionMu.Unlock()
	return nil
}

func (b *Bot) attachmentContent(ctx context.Context, assetMsg channel.IncomingMessage, a *discordgo.MessageAttachment) []ai.ContentBlock {
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
	if ok && assetMsg.Platform != "" {
		path, err := saver.SaveAsset(ctx, assetMsg, name, data)
		if err != nil {
			logger().Warn("save attachment failed", "attachment_id", a.ID, "file_name", name, "error", err)
		} else {
			return channel.AttachmentReceivedContent(name, path, data)
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
