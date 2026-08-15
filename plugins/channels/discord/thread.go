package discord

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

// messageRoute maps Discord threads onto Stella's parent-channel/thread pair.
// Discord sends replies to the thread channel itself; the parent ID is the
// stable group coordinate and the thread ID keeps forum posts isolated.
type messageRoute struct {
	chatID   string
	threadID string
}

func (b *Bot) resolveMessageRoute(ctx context.Context, m *discordgo.Message) (messageRoute, error) {
	route := messageRoute{chatID: m.ChannelID}
	if m.GuildID == "" {
		return route, nil
	}
	var ch *discordgo.Channel
	if b.session.State != nil {
		ch, _ = b.session.State.Channel(m.ChannelID)
	}
	if ch == nil && b.rest != nil {
		var err error
		ch, err = b.rest.Channel(m.ChannelID, discordgo.WithContext(ctx))
		if err != nil {
			return messageRoute{}, fmt.Errorf("resolve Discord channel %q: %w", m.ChannelID, err)
		}
	}
	if ch == nil {
		return messageRoute{}, fmt.Errorf("resolve Discord channel %q: empty response", m.ChannelID)
	}
	if ch.IsThread() && ch.ParentID == "" {
		return messageRoute{}, fmt.Errorf("resolve Discord thread %q: missing parent channel", m.ChannelID)
	}
	if ch.IsThread() {
		route.chatID = ch.ParentID
		route.threadID = ch.ID
	}
	return route, nil
}

// loadThreadHistory returns canonical platform messages to import into the
// group event log. Imported rows carry their original author and message ID and
// create no outbox work, so later mentions reuse history without duplicating a
// synthetic transcript in the current user's turn.
func (b *Bot) loadThreadHistory(ctx context.Context, current *discordgo.Message, route messageRoute) ([]channel.IncomingMessage, error) {
	if !b.cfg.RequireMention || route.threadID == "" || b.rest == nil {
		return nil, nil
	}
	messages, historyErr := b.rest.ChannelMessages(route.threadID, threadHistoryLimit, current.ID, "", "", discordgo.WithContext(ctx))
	if historyErr != nil {
		logger().Warn("read Discord thread history failed", "thread_id", route.threadID, "error", historyErr)
	}

	// A forum starter has the same ID as its thread. Fetch it directly because a
	// bounded recent-history window may not reach a long-running thread.
	var starter *discordgo.Message
	var starterErr error
	if current.ID != route.threadID {
		starter, starterErr = b.rest.ChannelMessage(route.threadID, route.threadID, discordgo.WithContext(ctx))
		if starterErr != nil {
			logger().Debug("read Discord thread starter failed", "thread_id", route.threadID, "error", starterErr)
		}
	}

	seen := map[string]struct{}{current.ID: {}}
	selected := make([]*discordgo.Message, 0, len(messages)+1)
	used := 0
	if b.importableDiscordHistoryMessage(starter) {
		seen[starter.ID] = struct{}{}
		selected = append(selected, starter)
		used += discordHistoryMessageSize(starter)
	}

	chronological := make([]*discordgo.Message, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if !b.importableDiscordHistoryMessage(message) {
			continue
		}
		if _, ok := seen[message.ID]; ok {
			continue
		}
		seen[message.ID] = struct{}{}
		chronological = append(chronological, message)
	}
	selectedFrom := len(chronological)
	for i := len(chronological) - 1; i >= 0; i-- {
		size := discordHistoryMessageSize(chronological[i])
		if used+size > threadContextMaxLen {
			break
		}
		used += size
		selectedFrom = i
	}
	selected = append(selected, chronological[selectedFrom:]...)

	if len(selected) == 0 && current.ID != route.threadID && historyErr != nil && starterErr != nil {
		return nil, fmt.Errorf("read Discord thread context: recent history: %w; starter: %w", historyErr, starterErr)
	}

	result := make([]channel.IncomingMessage, 0, len(selected))
	for _, message := range selected {
		blocks := discordHistoryContent(message)
		if len(blocks) == 0 {
			continue
		}
		if message.ChannelID == "" {
			message.ChannelID = route.threadID
		}
		im := b.incomingMessage(message, blocks, route.chatID, route.threadID)
		im.IsGroup = true
		result = append(result, im)
	}
	return result, nil
}

func (b *Bot) importableDiscordHistoryMessage(message *discordgo.Message) bool {
	if message == nil || message.ID == "" || message.Author == nil || message.Author.Bot {
		return false
	}
	// A historical bot mention or reply-to-bot is a turn in its own right.
	// Importing it without outbox work could win the platform-ID dedup race
	// against its live handler and permanently suppress that turn's response.
	return !b.addressed(message)
}

func discordHistoryMessageSize(message *discordgo.Message) int {
	size := len(message.Content) + 128
	for _, attachment := range message.Attachments {
		if attachment != nil {
			size += len(attachment.Filename) + 2
		}
	}
	return size
}

func discordHistoryContent(message *discordgo.Message) []ai.ContentBlock {
	body := strings.TrimSpace(message.Content)
	if len(message.Attachments) > 0 {
		names := make([]string, 0, len(message.Attachments))
		for _, attachment := range message.Attachments {
			if attachment != nil && attachment.Filename != "" {
				names = append(names, attachment.Filename)
			}
		}
		if len(names) > 0 {
			if body != "" {
				body += "\n"
			}
			body += "[Attachments: " + strings.Join(names, ", ") + "]"
		}
	}
	if body == "" {
		return nil
	}
	return []ai.ContentBlock{ai.TextContent{Text: body}}
}
