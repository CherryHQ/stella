package memory

import (
	"fmt"
	"time"

	"github.com/vaayne/anna/pkg/ai"
)

// Session identifies the context of a single conversation.
// It is created by Pool.Chat and passed to all Provider methods.
type Session struct {
	ID      string // unique session key (e.g. "default:cli:42:main")
	AgentID string // agent this session belongs to (e.g. "default")
	UserID  int64  // internal user ID (0 for anonymous/legacy)
	Channel string // originating channel (e.g. "cli", "telegram")
}

// SessionInfo holds metadata about a session.
type SessionInfo struct {
	ID         string
	AgentID    string
	UserID     int64
	Channel    string
	Title      string // auto-generated from first message
	CreatedAt  time.Time
	LastActive time.Time
	Archived   bool
}

// ListOptions controls session listing filters.
type ListOptions struct {
	AgentID         string // filter by agent (empty = all)
	UserID          int64  // filter by user (0 = all)
	IncludeArchived bool
	Limit           int // 0 = no limit
}

// EstimateTokens returns a rough token count (~4 chars per token).
func EstimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// MessageText extracts the plain text content from an ai.Message.
// Returns the text for user/assistant messages, or the tool result for tool messages.
func MessageText(msg ai.Message) string {
	switch m := msg.(type) {
	case ai.UserMessage:
		switch c := m.Content.(type) {
		case string:
			return c
		case []ai.ContentBlock:
			return ai.FlattenText(c)
		default:
			return fmt.Sprintf("%v", m.Content)
		}
	case ai.AssistantMessage:
		return ai.FlattenText(m.Content)
	case ai.ToolResultMessage:
		return ai.FlattenText(m.Content)
	default:
		return ""
	}
}

// MessageRole returns the role string for an ai.Message.
func MessageRole(msg ai.Message) string {
	switch msg.(type) {
	case ai.UserMessage:
		return "user"
	case ai.AssistantMessage:
		return "assistant"
	case ai.ToolResultMessage:
		return "tool"
	default:
		return "unknown"
	}
}

// MessageTimestamp returns the timestamp of an ai.Message.
func MessageTimestamp(msg ai.Message) time.Time {
	switch m := msg.(type) {
	case ai.UserMessage:
		return m.Timestamp
	case ai.AssistantMessage:
		return m.Timestamp
	case ai.ToolResultMessage:
		return m.Timestamp
	default:
		return time.Time{}
	}
}
