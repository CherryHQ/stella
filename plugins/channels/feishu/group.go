package feishu

import (
	"fmt"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/pkg/ai"
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

// shouldRespondInGroup checks whether the bot should respond based on group_mode
// and whether it was mentioned. Uses per-group config when available.
func (b *Bot) shouldRespondInGroup(chatID string, mentions []*larkim.MentionEvent) bool {
	switch b.groupMode(chatID) {
	case "disabled":
		return false
	case "always":
		return true
	default: // "mention"
		return b.isBotMentioned(mentions)
	}
}

// groupSystemPrompt returns the system prompt override for the given chat, if any.
func (b *Bot) groupSystemPrompt(chatID string) string {
	if gc, ok := b.cfg.Groups[chatID]; ok {
		return gc.SystemPrompt
	}
	return ""
}

// isGroupTrigger checks if a group message should trigger agent invocation.
// Unlike shouldRespondInGroup, "always" mode is treated as "mention" —
// the agent is only invoked on explicit @mentions, not every message.
func (b *Bot) isGroupTrigger(chatID string, mentions []*larkim.MentionEvent) bool {
	mode := b.groupMode(chatID)
	if mode == "disabled" {
		return false
	}
	if mode == "always" {
		b.warnAlwaysModeOnce(chatID)
	}
	return b.isBotMentioned(mentions)
}

// warnAlwaysModeOnce logs a one-time warning that "always" mode now behaves as "mention".
func (b *Bot) warnAlwaysModeOnce(chatID string) {
	if _, loaded := b.alwaysModeWarned.LoadOrStore(chatID, true); !loaded {
		logger().Warn("group_mode=always now behaves as mention — agent only invoked on @mention",
			"chat_id", chatID)
	}
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
