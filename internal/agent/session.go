package agent

import (
	"fmt"

	"github.com/vaayne/anna/pkg/memory"
)

// CompactionConfig controls automatic session compaction.
type CompactionConfig struct {
	// MaxTokens triggers compaction when the estimated token count exceeds this.
	// 0 (or omitted) uses the default of 80000. Negative values disable
	// automatic compaction. Manual /compact still works.
	MaxTokens int `yaml:"max_tokens"`
	// KeepTail is the number of recent message entries to preserve verbatim
	// after compaction. Default: 20.
	KeepTail int `yaml:"keep_tail"`
}

// WithDefaults returns a copy with zero-value fields replaced by defaults.
// MaxTokens 0 -> 80000; negative values are preserved (meaning disabled).
func (c CompactionConfig) WithDefaults() CompactionConfig {
	if c.MaxTokens == 0 {
		c.MaxTokens = 80_000
	}
	if c.KeepTail == 0 {
		c.KeepTail = 20
	}
	return c
}

// BuildSessionKey constructs a session key from agent, platform, user, and context.
// Format: {agentID}:{platform}:{externalUserID}:{channelContext}
func BuildSessionKey(agentID, platform, externalUserID, channelContext string) string {
	return agentID + ":" + platform + ":" + externalUserID + ":" + channelContext
}

// BuildUserSessionKey constructs a session key for a linked auth user.
// Linked users share a single session across all channels (for the same agent
// and channel context), so the key omits the platform-specific external ID.
// Format: {agentID}:user:{authUserID}:{channelContext}
func BuildUserSessionKey(agentID string, authUserID int64, channelContext string) string {
	return agentID + ":user:" + fmt.Sprintf("%d", authUserID) + ":" + channelContext
}

// BuildGroupSessionKey constructs a session key for a shared group chat.
// All members of the same group share a single history under this key.
// Format: {agentID}:group:{channelID}:{chatID}
func BuildGroupSessionKey(agentID, channelID, chatID string) string {
	return agentID + ":group:" + channelID + ":" + chatID
}

// SessionInfo is an alias for memory.SessionInfo.
type SessionInfo = memory.SessionInfo

// Session holds the state of a single conversation: metadata and the
// currently assigned  Message persistence is handled by the
// memory engine exclusively.
type Session struct {
	Info   SessionInfo
	Runner Runner
	Model  string // model ID the current runner was created with
}
