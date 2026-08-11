// Package channel defines the public contract for channel plugins.
// Channel plugins are thin platform adapters (Telegram, QQ, Feishu, Weixin)
// that normalise incoming messages, delegate business logic to a MessageHandler,
// and render streamed responses back to the platform.
package channel

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// Platform identifiers for each messaging channel.
const (
	PlatformTelegram = "telegram"
	PlatformDiscord  = "discord"
	PlatformQQ       = "qq"
	PlatformFeishu   = "feishu"
	PlatformWeixin   = "weixin"
	PlatformCLI      = "cli"

	// MaxInboundAttachmentBytes bounds one attachment before durable publication.
	MaxInboundAttachmentBytes = 32 << 20
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

// Handler is the message-routing contract used by channel plugins.
type Handler interface {
	MessageHandler
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
	ThreadID   string // platform sub-thread/topic id within ChatID (e.g. Telegram forum topic); empty if none
	Content    []ai.ContentBlock

	// Group-chat metadata (D3). Adapters fill what they can; empty/zero is allowed.
	MessageID string    // platform-native message ID (stable delivery/update id), empty if unavailable
	Timestamp time.Time // platform-reported send time, zero if unavailable
	ReplyTo   string    // platform message ID this message replies to, empty if none
	Mentions  []Mention // @-mentions, normalized; AgentID is resolved later by the dispatcher
}

// Mention is a normalized @-mention. Adapters fill Raw and PlatformID; the
// dispatcher resolves AgentID by looking up group membership. @-routing honors
// only mentions whose AgentID is non-empty.
type Mention struct {
	Raw        string // raw @ text (@username / <at open_id> ...), for audit/fallback
	PlatformID string // platform-side mentioned id (username / open_id / qq number)
	AgentID    string // resolved Stella agent; empty if unresolved
}

// ChatStream holds the event channel and session metadata returned by HandleMessage.
type ChatStream struct {
	Events    <-chan Event
	SessionID string
}

// Event is a stream event from the agent, consumed by channel plugins
// to render responses on the platform.
type Event struct {
	Text       string
	Reasoning  string
	Image      *ImageEvent
	File       *FileEvent
	ToolUse    *ToolUseEvent
	References []renderrefs.Reference
	Err        error
}

// ImageEvent carries a base64-encoded image.
type ImageEvent struct {
	Data     string // base64 encoded
	MimeType string // e.g. "image/jpeg"
}

// FileEvent carries a local file path to send to the user.
type FileEvent struct {
	Path string // absolute path on disk
	Name string // display filename (with extension)
}

// ToolUseEvent describes a tool invocation in progress or completed.
type ToolUseEvent struct {
	ID         string
	Tool       string // tool name, e.g. "bash", "read"
	Status     string // "running", "done", "error"
	Input      string // short summary of the tool input
	Arguments  map[string]any
	Detail     string // error detail or result summary
	Content    string
	References []renderrefs.Reference
}

// Notification is a push message to send to a chat.
type Notification struct {
	Channel     string // optional: route to a specific backend
	ChatID      string // target chat/channel within the backend
	RecipientID string // linked platform user identity; empty for explicit ChatID targets
	AgentID     string // optional: agent that produced the notification
	Text        string // markdown content
	Silent      bool   // send without notification sound
}

// AgentInfo is agent metadata for display in channel UIs.
type AgentInfo struct {
	ID   string
	Name string
}

// ProvisionRequest carries verified platform evidence for enrollment. Plugins
// remain independent of internal auth types; ExternalID is the platform's
// canonical identity subject (Feishu union_id for Feishu enrollment).
type ProvisionRequest struct {
	Platform   string
	ExternalID string
	TenantKey  string
	Email      string
	Name       string
	AvatarURL  string
}

// Provisioner is an optional capability that a Handler may implement.
// Channel plugins assert for this interface when they want to auto-provision
// users on first contact, without adding the method to every channel's Handler.
type Provisioner interface {
	ProvisionUser(ctx context.Context, req ProvisionRequest) error
}

// AssetSaveAdmitter authorizes attachment ingestion before a plugin downloads
// untrusted bytes. It deliberately exposes no workspace or host path.
type AssetSaveAdmitter interface {
	AdmitAssetSave(ctx context.Context, msg IncomingMessage) error
}

// BotRegistrar is an optional capability that a Handler may implement.
// Channel adapters call RegisterBotIdentity at startup to record their
// bot's platform identity (e.g., Telegram username), enabling the group
// dispatcher to resolve @mentions to Stella agents.
type BotRegistrar interface {
	RegisterBotIdentity(platform, platformBotID, channelID string)
}

// AssetSaver is an optional capability that a Handler may implement. Channel
// plugins assert for it to persist inbound bytes. Identity and workspace
// selection remain entirely host-owned.
type AssetSaver interface {
	// SaveAsset returns a portable $STELLA_ASSETS_DIR expression, never a host path.
	SaveAsset(ctx context.Context, msg IncomingMessage, fileName string, data []byte) (string, error)
}
