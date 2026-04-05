// Package channel defines the public contract for channel plugins.
// Channel plugins are thin platform adapters (Telegram, QQ, Feishu, Weixin)
// that normalise incoming messages, delegate business logic to a MessageHandler,
// and render streamed responses back to the platform.
package channel

import "context"

// Platform identifiers for each messaging channel.
const (
	PlatformTelegram = "telegram"
	PlatformQQ       = "qq"
	PlatformFeishu   = "feishu"
	PlatformWeixin   = "weixin"
	PlatformCLI      = "cli"
)

// Channel is a messaging platform adapter.
type Channel interface {
	// Name returns a unique identifier (e.g. "telegram", "qq").
	Name() string

	// Start begins listening for messages. Blocks until ctx is cancelled.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the channel.
	Stop()

	// Notify sends a push notification to a target within this channel.
	Notify(ctx context.Context, n Notification) error
}

// MessageHandler is the coordinator interface injected into channel plugins.
// It owns user resolution, agent routing, session management, and command handling.
type MessageHandler interface {
	// HandleMessage resolves the user, routes to an agent, and streams a response.
	HandleMessage(ctx context.Context, msg IncomingMessage) (*ChatStream, error)

	// HandleCommand processes shared commands (/start, /new, /compact, /whoami, /link).
	// Returns (response, handled). Unhandled commands return ("", false).
	HandleCommand(ctx context.Context, msg IncomingMessage, command, args string) (string, bool)

	// ListAgents returns enabled agents the user can access and the current agent ID.
	ListAgents(ctx context.Context, msg IncomingMessage) ([]AgentInfo, string, error)

	// SwitchAgent switches the active agent for this chat context.
	SwitchAgent(ctx context.Context, msg IncomingMessage, agentSlug string) error

	// ListModels returns available models.
	ListModels() []ModelOption

	// SwitchModel switches the active model.
	SwitchModel(provider, model string) error
}

// IncomingMessage is the normalised input from any platform.
type IncomingMessage struct {
	Platform   string // "telegram", "qq", etc.
	SenderID   string // platform-specific user ID
	SenderName string // display name
	ChatID     string // group/channel ID (empty for DMs)
	IsGroup    bool
	Content    any // string or []ai.ContentBlock
}

// ChatStream holds the event channel and session metadata returned by HandleMessage.
type ChatStream struct {
	Events    <-chan Event
	SessionID string
}

// Event is a stream event from the agent, consumed by channel plugins
// to render responses on the platform.
type Event struct {
	Text    string
	Image   *ImageEvent
	ToolUse *ToolUseEvent
	Err     error
}

// ImageEvent carries a base64-encoded image.
type ImageEvent struct {
	Data     string // base64 encoded
	MimeType string // e.g. "image/jpeg"
}

// ToolUseEvent describes a tool invocation in progress or completed.
type ToolUseEvent struct {
	Tool   string // tool name, e.g. "bash", "read"
	Status string // "running", "done", "error"
	Input  string // short summary of the tool input
	Detail string // error detail or result summary
}

// Notification is a push message to send to a chat.
type Notification struct {
	Channel string // optional: route to a specific backend
	ChatID  string // target chat/channel within the backend
	Text    string // markdown content
	Silent  bool   // send without notification sound
}

// AgentInfo is agent metadata for display in channel UIs.
type AgentInfo struct {
	ID   string
	Name string
}
