// Package channel defines the public contract for channel plugins.
// Channel plugins are thin platform adapters (Telegram, QQ, Feishu, DingTalk, Weixin)
// that normalise incoming messages, delegate business logic to a MessageHandler,
// and render streamed responses back to the platform.
package channel

import (
	"context"
	"errors"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// ErrJoinedChatListingUnavailable means the running channel cannot currently
// enumerate its joined group chats.
var ErrJoinedChatListingUnavailable = errors.New("channel: joined chat listing unavailable")

// Platform identifiers for each messaging channel.
const (
	PlatformTelegram = "telegram"
	PlatformDiscord  = "discord"
	PlatformQQ       = "qq"
	PlatformFeishu   = "feishu"
	PlatformDingTalk = "dingtalk"
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

// JoinedChat is a group chat the channel's bot currently belongs to.
// It deliberately contains only display metadata safe to expose in channel
// configuration, not platform-specific credentials or member data.
type JoinedChat struct {
	ID   string
	Name string
}

// JoinedChatPage is one opaque-token page of the bot's currently joined chats.
type JoinedChatPage struct {
	Chats         []JoinedChat
	NextPageToken string
}

// JoinedChatLister is an optional capability for channel adapters whose
// platform API can enumerate the groups the bot currently belongs to.
type JoinedChatLister interface {
	ListJoinedChats(ctx context.Context, pageSize int, pageToken string) (JoinedChatPage, error)
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
	MessageID         string    // platform-native message ID (stable delivery/update id), empty if unavailable
	Timestamp         time.Time // platform-reported send time, zero if unavailable
	ReplyTo           string    // platform message ID this message replies to, empty if none
	Mentions          []Mention // @-mentions, normalized; AgentID is resolved later by the dispatcher
	LifecycleFeedback bool      // platform adapter should show addressed-turn completion feedback
}

// Mention is a normalized @-mention. Adapters fill Raw and PlatformID; the
// dispatcher resolves AgentID by looking up group membership. @-routing honors
// only mentions whose AgentID is non-empty.
// The tags are deliberately the Go field names, not snake_case: mentions are
// persisted inside the ctx_group_outbox envelope JSON, and the
// AgentMentionedSinceCursor query in internal/db/queries/ctx_group_dispatch.sql
// matches `AgentID` inside that stored JSON. The tags freeze the wire names so
// renaming a field here can never silently break that query against old rows.
type Mention struct {
	Raw        string `json:"Raw"`        // raw @ text (@username / <at open_id> ...), for audit/fallback
	PlatformID string `json:"PlatformID"` // platform-side mentioned id (username / open_id / qq number)
	AgentID    string `json:"AgentID"`    // resolved Stella agent; empty if unresolved
}

// ChatStream holds the event channel and session metadata returned by HandleMessage.
type ChatStream struct {
	Events    <-chan Event
	SessionID string

	// Delivery authorizes every model-derived external send for this turn and
	// records what became of it. It is present exactly when the turn's committed
	// output has a durable delivery owner: a Run-output receipt for a direct
	// channel turn, or the group dispatch row for a published group reply.
	//
	// It is nil only for streams with no external delivery claim at all (Web/SSE,
	// a webhook HTTP response, tests). That is a statement about the stream, not a
	// global no-op: a stream that can reach a platform always carries one.
	Delivery Delivery
}

// DeliveryResult is the terminal result of one channel-side delivery attempt.
// Only DeliveryNotSent proves that no external request was started; DeliveryUnknown
// means the platform may have accepted the bytes and must never be retried
// transparently.
type DeliveryResult string

const (
	DeliverySent    DeliveryResult = "sent"
	DeliveryNotSent DeliveryResult = "not_sent"
	DeliveryUnknown DeliveryResult = "unknown"
)

// SendKind distinguishes what is being sent, because the two are authorized
// differently and only one of them is model output.
type SendKind uint8

const (
	// SendOutput is model-derived reply content: streamed deltas and the final
	// rendered answer. While the Run executes it needs valid execution ownership;
	// afterwards it needs the committed output record of that same turn.
	SendOutput SendKind = iota
	// SendControl is a message the channel adapter itself composed — an error,
	// a timeout, or a status notice. It is authorized by this turn's delivery
	// ownership rather than by committed model output, so a failed or canceled
	// turn can still tell the user what happened.
	SendControl
)

// Delivery authorizes and settles the external delivery of one committed turn.
type Delivery interface {
	// Authorize must be called immediately before each external request. It fails
	// when this stream no longer has authority to send: while the Run is live the
	// execution fence decides model output, and once it is terminal the committed
	// output's own delivery record decides. A canceled, expired, or superseded
	// producer therefore cannot send either way.
	Authorize(ctx context.Context, kind SendKind) error
	// Settle records the terminal delivery result exactly once, after the last
	// external request. Streams whose delivery outcome is owned by a durable
	// dispatcher row still settle: the call confirms this attempt still owns the
	// row and releases the source queue slot.
	Settle(ctx context.Context, result DeliveryResult) error
	// Done closes once the delivery attempt has settled, which is when a per-chat
	// FIFO may admit the next turn. It never reports Run execution state.
	Done() <-chan struct{}
}

// DeliveryResultForError classifies a send-path error after an external request
// may have started. Every non-nil error is unknown: a timeout or cancellation
// does not prove that the platform failed to receive the bytes. Callers use
// DeliveryNotSent only when they refused to start the request at all.
func DeliveryResultForError(err error) DeliveryResult {
	if err == nil {
		return DeliverySent
	}
	return DeliveryUnknown
}

// AuthorizeSend runs the stream's per-send authority check. A stream with no
// delivery owner has no external effect to authorize.
func (s *ChatStream) AuthorizeSend(ctx context.Context, kind SendKind) error {
	if s == nil || s.Delivery == nil {
		return nil
	}
	return s.Delivery.Authorize(ctx, kind)
}

// Settle reports the terminal delivery result for this stream.
func (s *ChatStream) Settle(ctx context.Context, result DeliveryResult) error {
	if s == nil || s.Delivery == nil {
		return nil
	}
	return s.Delivery.Settle(ctx, result)
}

// DeliveryDone is the per-chat FIFO release barrier. A stream with no delivery
// owner released its slot already.
func (s *ChatStream) DeliveryDone() <-chan struct{} {
	if s == nil || s.Delivery == nil {
		return alreadyComplete
	}
	return s.Delivery.Done()
}

var alreadyComplete = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// Discard drains a stream asynchronously after a channel stops publishing it.
// Model execution must never remain blocked on a full event buffer merely
// because the outbound path gave up.
func (s *ChatStream) Discard() {
	if s == nil || s.Events == nil {
		return
	}
	go func() {
		for range s.Events {
		}
	}()
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

// EnrollmentRequest is the platform-neutral account enrollment contract.
// The host supplies the namespace separately; plugins cannot select it.
type EnrollmentRequest struct {
	Subject string
	Email   string
	// EmailSynthetic records that the adapter supplied a compatibility email;
	// the host persists the marker as a boolean claim without deriving it.
	EmailSynthetic bool
	Name           string
	AvatarURL      string
	Claims         map[string]string
}

// AccountEnroller is an independent host capability. Message handlers do not
// carry account write authority.
type AccountEnroller interface {
	EnrollAccount(ctx context.Context, req EnrollmentRequest) error
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

// BotNameRegistrar is an optional capability that a Handler may implement.
// It exists for platforms whose bot ids are scoped to the receiving app
// (Feishu open_id), where no id lets one app recognise another app's bot and
// the display name is the only shared identity.
type BotNameRegistrar interface {
	RegisterBotName(platform, displayName, channelID string)
}

// AssetSaver is an optional capability that a Handler may implement. Channel
// plugins assert for it to persist inbound bytes. Identity and workspace
// selection remain entirely host-owned.
type AssetSaver interface {
	// SaveAsset returns a portable $STELLA_ASSETS_DIR expression, never a host path.
	SaveAsset(ctx context.Context, msg IncomingMessage, fileName string, data []byte) (string, error)
}
