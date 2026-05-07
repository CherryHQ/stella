package feishu

import (
	"context"
	"fmt"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/channel"
)

// attributeGroupContent prepends a "[Name]: " prefix to text content blocks
// so the agent knows who is speaking. Falls back to openID if no name is cached.
func (b *Bot) attributeGroupContent(openID string, content []ai.ContentBlock) []ai.ContentBlock {
	name := b.cachedName(openID)
	if name == "" {
		name = openID
	}
	prefix := ai.TextContent{Text: fmt.Sprintf("[%s]: ", name)}
	return append([]ai.ContentBlock{prefix}, content...)
}

// groupMode returns the effective group mode for the given chat.
// Per-group config overrides the global setting.
func (b *Bot) groupMode(chatID string) string {
	if gc, ok := b.cfg.Groups[chatID]; ok && gc.GroupMode != "" {
		return gc.GroupMode
	}
	return b.cfg.GroupMode
}

// groupSystemPrompt returns the system prompt override for the given chat, if any.
func (b *Bot) groupSystemPrompt(chatID string) string {
	if gc, ok := b.cfg.Groups[chatID]; ok {
		return gc.SystemPrompt
	}
	return ""
}

// isGroupTrigger checks if a group message should trigger agent invocation.
//   - "always"  — every message triggers the agent
//   - "mention" — only @bot mentions trigger
//   - "disabled" — never triggers
func (b *Bot) isGroupTrigger(chatID string, mentions []*larkim.MentionEvent) bool {
	switch b.groupMode(chatID) {
	case "disabled":
		return false
	case "always":
		return true
	default:
		return b.isBotMentioned(mentions)
	}
}

// decorateGroupCtx builds a context carrying the group log, system prompt,
// and notification reply targeting for a group invocation.
// If groupLogText is non-empty it is used directly; otherwise the ring buffer
// is formatted on the fly.
func (b *Bot) decorateGroupCtx(chatID, messageID, rootID, groupLogText string) context.Context {
	ctx := context.Background()

	if groupLogText == "" {
		groupLogText = b.groupLog(chatID).FormatContext(50, b.cachedName)
	}

	// Prepend per-group system prompt if configured.
	if sp := b.groupSystemPrompt(chatID); sp != "" {
		if groupLogText != "" {
			groupLogText = sp + "\n\n" + groupLogText
		} else {
			groupLogText = sp
		}
	}

	groupLogText += "\n\nTo reply in this group conversation, use the notify tool."

	ctx = agent.WithGroupContext(ctx, groupLogText)
	ctx = channel.WithNotificationReply(ctx, channel.NotificationReplyContext{
		ChatID:    chatID,
		MessageID: threadReplyTarget(messageID, rootID),
	})
	return ctx
}

// isBotMentioned checks if the bot was @mentioned by comparing each mention's
// open_id against the bot's own open_id (fetched on startup).
func (b *Bot) isBotMentioned(mentions []*larkim.MentionEvent) bool {
	if len(mentions) == 0 {
		return false
	}

	knownID, _ := b.botOpenID.Load().(string)
	if knownID == "" {
		// Fallback: if bot open_id is unknown, assume any non-@all mention is the bot.
		for _, m := range mentions {
			if m.Key != nil && *m.Key != "@_all" {
				return true
			}
		}
		return false
	}

	for _, m := range mentions {
		if m.Id == nil {
			continue
		}
		if m.Id.OpenId != nil && *m.Id.OpenId == knownID {
			return true
		}
	}
	return false
}
