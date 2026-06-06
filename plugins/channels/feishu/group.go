package feishu

import (
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// groupMode returns the effective group mode for the given chat.
// Per-group config overrides the global setting.
func (b *Bot) groupMode(chatID string) string {
	if gc, ok := b.cfg.Groups[chatID]; ok && gc.GroupMode != "" {
		return gc.GroupMode
	}
	return b.cfg.GroupMode
}

func (b *Bot) shouldIngestGroup(chatID string) bool {
	return b.groupMode(chatID) != "disabled"
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
