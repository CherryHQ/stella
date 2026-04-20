// Package channel defines the public contract for channel plugins.
// Channel plugins are thin platform adapters (Telegram, QQ, Feishu, Weixin)
// that normalise incoming messages, delegate business logic to a MessageHandler,
// and render streamed responses back to the platform.
package channel

import (
	"context"

	"github.com/vaayne/anna/pkg/ai"
)

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

// MessageHandler is the core coordinator interface injected into channel plugins.
// It owns user resolution, agent routing, session management, and command handling.
// The single HandleIncoming entry point resolves the user once, tries command
// handling, and falls through to chat streaming — eliminating double resolution.
type MessageHandler interface {
	// HandleIncoming resolves the user once, tries command handling, and if the
	// command is not handled, streams a chat response.
	// Returns (commandResponse, handled, stream, err).
	// If handled is true, commandResponse contains the reply and stream is nil.
	// If handled is false, stream contains the chat response.
	HandleIncoming(ctx context.Context, msg IncomingMessage, command, args string) (string, bool, *ChatStream, error)
}

// ModelManager provides model listing and switching for channel plugins
// that support the /model command.
type ModelManager interface {
	// ListModels returns available models.
	ListModels() []ModelOption

	// SwitchModel switches the active model.
	SwitchModel(provider, model string) error
}

// AgentManager provides agent listing and switching for channel plugins
// that support the /agent command.
type AgentManager interface {
	// ListAgents returns enabled agents the user can access and the current agent ID.
	ListAgents(ctx context.Context, msg IncomingMessage) ([]AgentInfo, string, error)

	// SwitchAgent switches the active agent for this chat context.
	SwitchAgent(ctx context.Context, msg IncomingMessage, agentSlug string) error
}

// Handler combines message routing with model and agent management.
// Channel plugins typically need all three capabilities.
type Handler interface {
	MessageHandler
	ModelManager
	AgentManager
}

// IncomingMessage is the normalised input from any platform.
type IncomingMessage struct {
	Platform   string   // "telegram", "qq", etc.
	ChannelID  string   // configured channel instance ID; defaults to Platform.
	SenderID   string   // preferred platform-specific user ID
	SenderIDs  []string // ordered candidate sender IDs, most stable first
	SenderName string   // display name
	ChatID     string   // group/channel ID (empty for DMs)
	IsGroup    bool
	Content    []ai.ContentBlock
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
	AgentID string // optional: agent that produced the notification
	Text    string // markdown content
	Silent  bool   // send without notification sound
}

// AgentInfo is agent metadata for display in channel UIs.
type AgentInfo struct {
	ID   string
	Name string
}

// ProvisionRequest carries the information needed to auto-provision a channel user.
type ProvisionRequest struct {
	Platform   string
	ExternalID string
	Name       string
	EmailHint  string
}

// Provisioner is an optional capability that a Handler may implement.
// Channel plugins assert for this interface when they want to auto-provision
// users on first contact, without adding the method to every channel's Handler.
type Provisioner interface {
	ProvisionUser(ctx context.Context, req ProvisionRequest) error
}
