package channel

import "sync"

// BotIdentityRegistry is an in-memory map from a bot's platform identity
// (e.g., Telegram username) to the channel instance that owns that bot.
// Adapters populate it at startup; the group dispatcher reads it to resolve
// @-mentions to Stella agents.
type BotIdentityRegistry struct {
	mu      sync.RWMutex
	entries map[string]string // "platform\x00platformBotID" → channelID
}

// NewBotIdentityRegistry returns an empty registry.
func NewBotIdentityRegistry() *BotIdentityRegistry {
	return &BotIdentityRegistry{entries: make(map[string]string)}
}

// Register records that the given platform bot identity belongs to channelID.
func (r *BotIdentityRegistry) Register(platform, platformBotID, channelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[platform+"\x00"+platformBotID] = channelID
}

// Unregister removes a bot identity only when it still belongs to channelID.
func (r *BotIdentityRegistry) Unregister(platform, platformBotID, channelID string) {
	key := platform + "\x00" + platformBotID
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[key] == channelID {
		delete(r.entries, key)
	}
}

// ChannelIDForBot returns the channel that owns the bot identified by
// (platform, platformBotID). Returns ("", false) if unknown.
func (r *BotIdentityRegistry) ChannelIDForBot(platform, platformBotID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.entries[platform+"\x00"+platformBotID]
	return id, ok
}
