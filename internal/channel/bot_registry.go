package channel

import (
	"strings"
	"sync"
)

// BotIdentityRegistry is an in-memory map from a bot's platform identity
// (e.g., Telegram username) to the channel instance that owns that bot.
// Adapters populate it at startup; the group dispatcher reads it to resolve
// @-mentions to Stella agents.
type BotIdentityRegistry struct {
	mu      sync.RWMutex
	entries map[string]string // "platform\x00platformBotID" → channelID
	// names is the cross-app fallback. A Feishu open_id is scoped to the app
	// that receives the event, so one bot has a different id in every other
	// app's view and no id-based lookup can span them. The display name is the
	// only join key the platform leaves us, so it is matched case-insensitively
	// and only after the id lookup misses.
	names map[string]string // "platform\x00lower(displayName)" → channelID
}

// NewBotIdentityRegistry returns an empty registry.
func NewBotIdentityRegistry() *BotIdentityRegistry {
	return &BotIdentityRegistry{entries: make(map[string]string), names: make(map[string]string)}
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

// RegisterName records the bot's platform display name as a fallback identity
// for channelID. Registering the same name from two channels is ambiguous, so
// the second registration disables the name instead of guessing.
func (r *BotIdentityRegistry) RegisterName(platform, displayName, channelID string) {
	key := nameKey(platform, displayName)
	if key == "" || channelID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner, exists := r.names[key]; exists && owner != channelID {
		r.names[key] = ""
		return
	}
	r.names[key] = channelID
}

// UnregisterName removes a display name only when it still belongs to channelID.
func (r *BotIdentityRegistry) UnregisterName(platform, displayName, channelID string) {
	key := nameKey(platform, displayName)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[key] == channelID {
		delete(r.names, key)
	}
}

// ChannelIDForBotName returns the channel that owns the bot with this platform
// display name. An ambiguous name resolves to ("", false).
func (r *BotIdentityRegistry) ChannelIDForBotName(platform, displayName string) (string, bool) {
	key := nameKey(platform, displayName)
	if key == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.names[key]
	if id == "" {
		return "", false
	}
	return id, ok
}

func nameKey(platform, displayName string) string {
	name := strings.ToLower(strings.TrimSpace(displayName))
	if platform == "" || name == "" {
		return ""
	}
	return platform + "\x00" + name
}

// ChannelIDForBot returns the channel that owns the bot identified by
// (platform, platformBotID). Returns ("", false) if unknown.
func (r *BotIdentityRegistry) ChannelIDForBot(platform, platformBotID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.entries[platform+"\x00"+platformBotID]
	return id, ok
}
