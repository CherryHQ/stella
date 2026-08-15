package discord

import (
	"context"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

// nativeCommands lists the Discord application (slash) commands registered on
// activation. Every one of them maps onto a text command handleMessage
// already parses; registering them is a discoverability convenience, never a
// second implementation of command routing (see runCommandInteraction).
func nativeCommands() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{Name: "help", Description: "Show Stella's help message."},
		{Name: "start", Description: "Show Stella's welcome message."},
		{Name: "new", Description: "Start a new Stella session in this chat."},
		{Name: "compact", Description: "Compact the current Stella session."},
		{Name: "abort", Description: "Abort Stella's active response in this chat."},
		{Name: "whoami", Description: "Show your Discord user ID."},
		{Name: "link", Description: "Link this Discord account to a Stella user.", Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "code", Description: "Link code from the admin profile page.", Required: true},
		}},
	}
}

// registerNativeCommands is best-effort: a registration failure leaves the
// bot on text commands only, which handleMessage already serves regardless
// of whether native commands ever registered.
func (b *Bot) registerNativeCommands(ctx context.Context) {
	if b.rest == nil || b.botID == "" {
		return
	}
	if _, err := b.rest.ApplicationCommandBulkOverwrite(b.botID, "", nativeCommands(), discordgo.WithContext(ctx)); err != nil {
		logger().Warn("register discord native slash commands failed; text commands remain available", "error", err)
	}
}

func (b *Bot) onInteractionCreate(_ *discordgo.Session, event *discordgo.InteractionCreate) {
	if event == nil || event.Interaction == nil {
		return
	}
	b.mu.RLock()
	ctx := b.ctx
	b.mu.RUnlock()
	if ctx == nil {
		return
	}
	switch event.Type {
	case discordgo.InteractionApplicationCommand:
		b.handleCommandInteraction(ctx, event.Interaction)
	case discordgo.InteractionMessageComponent:
		b.handleComponentInteraction(ctx, event.Interaction)
	}
}

// interactionUser returns the invoking user's ID and display name. Discord
// fills exactly one of Member/User depending on whether the interaction
// happened in a guild or a DM (see discordgo.Interaction's doc comments).
func interactionUser(ix *discordgo.Interaction) (id, name string) {
	u := ix.User
	if ix.Member != nil && ix.Member.User != nil {
		u = ix.Member.User
	}
	if u == nil {
		return "", ""
	}
	name = u.GlobalName
	if name == "" {
		name = u.Username
	}
	return u.ID, name
}

// authorizeInteractionRoute re-applies handleMessage's own admission gate —
// DM availability, and for guild interactions the guild/channel/user/role
// allowlist — to an interaction. Discord delivers a global command's
// interactions from every guild the bot has joined regardless of this bot
// instance's configured allowlist, so skipping this check would let native
// commands and Cancel clicks bypass the exact gate typed messages must pass.
func (b *Bot) authorizeInteractionRoute(ctx context.Context, guildID, channelID, userID string, member *discordgo.Member) (bool, messageRoute, error) {
	if guildID == "" {
		if !b.cfg.AllowDM {
			return false, messageRoute{}, nil
		}
		return true, messageRoute{chatID: channelID}, nil
	}
	synthetic := &discordgo.Message{ChannelID: channelID, GuildID: guildID, Author: &discordgo.User{ID: userID}, Member: member}
	return b.groupAccessAllowed(ctx, synthetic)
}

func (b *Bot) respondEphemeral(ix *discordgo.Interaction, content string) {
	if b.rest == nil {
		return
	}
	if err := b.rest.InteractionRespond(ix, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		logger().Warn("respond to discord interaction failed", "error", err)
	}
}

// handleCommandInteraction acknowledges within Discord's 3-second window by
// deferring an ephemeral response unconditionally, then edits it once the
// command has actually run. This uniform defer+edit shape means no command's
// latency (a DB round trip, a session queue wait) can blow the ACK deadline,
// at the cost of every native-command reply being ephemeral.
func (b *Bot) handleCommandInteraction(ctx context.Context, ix *discordgo.Interaction) {
	if b.rest == nil {
		return
	}
	if err := b.rest.InteractionRespond(ix, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		logger().Warn("ack discord command interaction failed", "error", err)
		return
	}
	reply := b.runCommandInteraction(context.WithoutCancel(ctx), ix)
	if _, err := b.rest.InteractionResponseEdit(ix, &discordgo.WebhookEdit{Content: &reply}); err != nil {
		logger().Warn("edit discord command interaction response failed", "error", err)
	}
}

// runCommandInteraction resolves a slash command to its final reply by
// routing through the exact same channel.Handler.HandleIncoming entry point
// handleMessage uses for typed commands — same command handling, same group
// security semantics (allowlist, /config and /new refusal in groups, event-log
// ingestion for anything else), no parallel implementation of any of it.
func (b *Bot) runCommandInteraction(ctx context.Context, ix *discordgo.Interaction) string {
	data := ix.ApplicationCommandData()
	cmd := "/" + data.Name
	args := ""
	if len(data.Options) > 0 && data.Options[0].Type == discordgo.ApplicationCommandOptionString {
		args = data.Options[0].StringValue()
	}
	userID, userName := interactionUser(ix)
	if userID == "" {
		return "Could not identify the Discord user for this command."
	}
	allowed, route, err := b.authorizeInteractionRoute(ctx, ix.GuildID, ix.ChannelID, userID, ix.Member)
	if err != nil {
		logger().Warn("authorize discord command interaction failed", "command", cmd, "error", err)
		return "Stella could not process this command right now."
	}
	if !allowed {
		return "This command is not available here."
	}
	text := cmd
	if args != "" {
		text += " " + args
	}
	msg := channel.IncomingMessage{
		Platform:   channel.PlatformDiscord,
		ChannelID:  b.Name(),
		SenderID:   userID,
		SenderName: userName,
		ChatID:     route.chatID,
		IsGroup:    ix.GuildID != "",
		ThreadID:   route.threadID,
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
	}
	resp, handled, stream, err := b.handler.HandleIncoming(ctx, msg, cmd, args)
	if err != nil {
		logger().Warn("discord command interaction failed", "command", cmd, "error", err)
		return "Stella could not process this command."
	}
	if handled {
		return resp
	}
	if stream != nil {
		// No registered native command resolves to a live chat stream today
		// (all seven are commands or an unsupported-in-group refusal); drain
		// defensively so a future one that did would not leak a goroutine
		// instead of surfacing a reply here.
		go func() {
			for range stream.Events {
			}
		}()
	}
	return "Received."
}

// handleComponentInteraction handles a Cancel button click. It authorizes
// twice: the same guild/channel allowlist gate as everything else, then the
// registry entry's requester — only the human whose message started the turn
// may stop it. Either failure responds ephemeral without side effects, and an
// unknown token (already finished, or the registry lost it to a restart) is
// reported as ended rather than denied, since nothing is left to deny.
func (b *Bot) handleComponentInteraction(ctx context.Context, ix *discordgo.Interaction) {
	if b.rest == nil {
		return
	}
	data := ix.MessageComponentData()
	if data.ComponentType != discordgo.ButtonComponent || !strings.HasPrefix(data.CustomID, cancelCustomIDPrefix) {
		return
	}
	token := strings.TrimPrefix(data.CustomID, cancelCustomIDPrefix)
	userID, _ := interactionUser(ix)
	allowed, _, err := b.authorizeInteractionRoute(ctx, ix.GuildID, ix.ChannelID, userID, ix.Member)
	if err != nil {
		logger().Warn("authorize discord cancel interaction failed", "error", err)
		b.respondEphemeral(ix, "Stella could not process this action right now.")
		return
	}
	if !allowed {
		b.respondEphemeral(ix, "This action is not available here.")
		return
	}
	entry, ok := b.cancels.get(token)
	if !ok {
		b.respondEphemeral(ix, "This action has already ended.")
		return
	}
	if userID == "" || entry.requesterID == "" || userID != entry.requesterID {
		b.respondEphemeral(ix, "Only the requester can cancel this.")
		return
	}
	if err := b.rest.InteractionRespond(ix, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: "Stopping…", Components: []discordgo.MessageComponent{}},
	}); err != nil {
		logger().Warn("ack discord cancel interaction failed", "error", err)
		return
	}
	b.cancels.unregister(token)
	// Abort runs after the ACK, off the interaction's 3-second clock: for a
	// DM it re-resolves the session (a DB round trip) before cancelling.
	go entry.abort()
}
