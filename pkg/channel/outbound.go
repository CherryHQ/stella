package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// OutboundAddress fixes where one durable outbound operation lands: the
// physical platform chat coordinates plus the reply anchor, frozen at
// enqueue time. Senders replay it verbatim; they never re-resolve identity.
type OutboundAddress struct {
	ChatKey    string `json:"chat_key"`
	ThreadKey  string `json:"thread_key,omitempty"`
	ReplyToKey string `json:"reply_to_key,omitempty"`
	// Token carries the platform reply credential when one is required —
	// Weixin's context_token authorizes a reply to a conversation.
	Token string `json:"token,omitempty"`
	// Scope marks the platform conversation kind when the chat key alone is
	// ambiguous (e.g. QQ "group"/"c2c").
	Scope string `json:"scope,omitempty"`
}

// OutboundOp is one channel_outbox row handed to the owning adapter. Payload
// is the frozen operation body (e.g. TextPayload JSON); the adapter decodes
// only the kinds it supports.
type OutboundOp struct {
	Kind           string
	DeliveryKey    string
	OperationIndex int
	Address        OutboundAddress
	Payload        json.RawMessage
	// ID is the op's channel_outbox row id — the send boundary resolves a
	// file attachment's bytes by it.
	ID string
	// SourceAccountKey is the platform account the sending channel was bound
	// to when the op was accepted; an adapter whose identity differs must not
	// send it (account_mismatch). For DM replies it is the receiving account;
	// for group replies it is the reply channel's registered account snapshot
	// (channel.runtime_account_key), which may differ from the trigger's
	// observing account. Empty means unfenced (no account was bound).
	SourceAccountKey string
	// DraftMessageID is the platform message id of the run's live draft,
	// recorded by the newest sent draft_update. The dispatcher populates it on
	// draft_update and send_reply ops so progress edits and the final version
	// all land on one platform message — create identity stays stable across
	// retries and owner handoffs. It is empty when the chain is polluted: a
	// draft op whose outcome is 'unknown' may still have its edit in flight
	// (stalled owner, delayed platform apply), so every later op must create
	// a fresh message instead of letting a zombie overwrite it. The abandoned
	// preview may linger or land late; the final reply can never be reverted.
	DraftMessageID string
	// Guard re-validates that this replica still owns the channel lease. It is
	// populated at claim time and never serialized; senders whose op performs
	// more than one platform call must invoke it before each call after the
	// first. A single-call op needs no check — claim admission already fenced it.
	Guard func(ctx context.Context) error `json:"-"`
}

// CheckOwnership runs the lease guard before an additional external call.
// Returns nil when no guard is installed; a lease loss is classified
// retryable so the op returns to pending for the new owner instead of being
// sent by a fenced-out replica.
func (o OutboundOp) CheckOwnership(ctx context.Context) error {
	if o.Guard == nil {
		return nil
	}
	if err := o.Guard(ctx); err != nil {
		return SendErrorf(SendRetryable, "channel ownership lost mid-operation: %v", err)
	}
	return nil
}

// SendResult is the confirmed platform receipt of one operation.
type SendResult struct {
	// PlatformMessageID is the platform-native id of the created message,
	// when the platform returns one.
	PlatformMessageID string
}

// SendClass decides how the ledger treats a failed send.
type SendClass int

const (
	// SendRetryable: the platform provably did not record the operation
	// (rate limit, 5xx, refused connection). Safe to retry.
	SendRetryable SendClass = iota
	// SendPermanent: the platform rejected the operation (4xx, invalid
	// target). Retrying cannot succeed.
	SendPermanent
	// SendUnknown: the response never arrived (timeout, EOF mid-response).
	// The platform may have recorded it — blind resend could duplicate.
	SendUnknown
)

// SendError wraps a platform send failure with its ledger classification.
// Adapters construct it; the dispatcher maps it to an outcome.
type SendError struct {
	Class SendClass
	Err   error
}

func (e *SendError) Error() string { return e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

func SendErrorf(class SendClass, format string, args ...any) *SendError {
	return &SendError{Class: class, Err: fmt.Errorf(format, args...)}
}

// SendClassify returns the send class of err: SendError wins, then ctx
// cancellation/deadline, defaulting to unknown (safer than blind resend).
func SendClassify(err error) SendClass {
	var se *SendError
	if errors.As(err, &se) {
		return se.Class
	}
	return SendUnknown
}

// OperationSender is the capability a running channel adapter exposes for the
// durable outbox: one deterministic platform call per operation, returning a
// platform receipt or a classified SendError.
type OperationSender interface {
	SendOperation(ctx context.Context, op OutboundOp) (SendResult, error)
}

// AccountChecker is an optional OperationSender capability: report whether
// this adapter instance still speaks for the op's source account. When the
// channel's credentials were swapped to a different bot after the op was
// produced, OwnsAccount returns false and the op fails account_mismatch —
// the new bot must never send another account's replies.
type AccountChecker interface {
	OwnsAccount(accountKey string) bool
}

// DraftSender is an optional OperationSender capability: the platform can
// maintain one message in place while the run is still executing. The first
// call has an empty op.DraftMessageID and creates the draft; later calls edit
// that message. The terminal send_reply op carries the same DraftMessageID
// and must land the final version on it, so a stale draft edit can never
// outlive the reply.
type DraftSender interface {
	SendDraftUpdate(ctx context.Context, op OutboundOp) (SendResult, error)
}

// DraftUpdatePayload is the frozen body of a "draft_update" op — the
// coalesced progress snapshot: full rendered text so far plus the event
// sequence it covers. Snapshots are self-contained, never deltas.
type DraftUpdatePayload struct {
	V    int    `json:"v"`
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
}

// GroupReplyOpPayload is the frozen body of the "send_group_reply" outbox op —
// everything the sender needs to deliver an accepted group reply on the
// replica that owns the channel lease. Text is the pre-split primary segment;
// overflow segments and attachments are sibling send_text / send_attachment
// ops, so this op is one platform call with one receipt. Events stay for
// platforms whose primary artifact renders richer than a text segment
// (Feishu cards carry reference sections).
type GroupReplyOpPayload struct {
	V                int     `json:"v"`
	Platform         string  `json:"platform"`
	PlatformGroupID  string  `json:"platform_group_id"`
	PlatformThreadID string  `json:"platform_thread_id,omitempty"`
	ReplyTo          string  `json:"reply_to,omitempty"`
	DeliveryID       string  `json:"delivery_id"`
	RequesterID      string  `json:"requester_id,omitempty"`
	SessionID        string  `json:"session_id,omitempty"`
	Events           []Event `json:"events"`
	// LifecycleFeedback gates the ack/terminal reaction pair on platforms
	// that support it; ambient turns must not receive unsolicited reactions.
	LifecycleFeedback bool   `json:"lifecycle_feedback,omitempty"`
	Text              string `json:"text"`
}

// ReplyOpPayload is the frozen body of the "send_reply" outbox op — the whole
// recorded turn event list, so the owning adapter can replay it through its
// draft/edit machinery (or flatten it to text) on whichever replica holds the
// channel lease.
type ReplyOpPayload struct {
	V         int     `json:"v"`
	SessionID string  `json:"session_id,omitempty"`
	Events    []Event `json:"events"`
	// Text is the pre-split message segment this op must deliver as its
	// primary platform message. Overflow segments are sibling send_text ops
	// and every attachment is its own send_attachment op, so a mid-delivery
	// retry never resends a segment that already landed. Empty means the
	// adapter's primary artifact carries no message text of its own (e.g.
	// QQ's terminal stream chunk).
	Text string `json:"text,omitempty"`
}

// AttachmentOpPayload is the frozen body of a "send_attachment" op: exactly
// one platform upload/send call for one file or image. Image data travels
// inline as base64; file content is read lazily through the op's row id at
// send time, so a replay on any replica reads the same immutable bytes.
type AttachmentOpPayload struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"` // "image" | "file"
	Name     string `json:"name,omitempty"`
	Data     string `json:"data,omitempty"` // base64, image kind
	MimeType string `json:"mime_type,omitempty"`
}

// AttachmentOpener materializes an op's file bytes at the send boundary. The
// Coordinator implements it over channel_outbox_attachment so a send on any
// replica reads the same immutable bytes committed with the op.
type AttachmentOpener interface {
	OpenAttachment(ctx context.Context, outboxID string) ([]byte, error)
}

// ErrAttachmentMissing marks a structurally unresolvable attachment op: no
// row id, or a handler that cannot open one. Senders map it to a
// permanent failure — retrying can never conjure the reference. A failure
// inside OpenAttachment itself stays retryable: the store may recover.
var ErrAttachmentMissing = errors.New("attachment body unavailable")

// OpenAttachmentOp resolves a file-kind send_attachment op to bytes at
// the send boundary. The handler must implement AttachmentOpener — the
// production Coordinator does. The store read can outlive the replica's lease,
// so ownership is re-checked after the read and before the bytes return: a
// sender that holds the result may still issue its platform call, but a
// fenced-out replica never reaches that point.
func OpenAttachmentOp(ctx context.Context, handler Handler, op OutboundOp) ([]byte, error) {
	if op.ID == "" {
		return nil, fmt.Errorf("%w: op carries no row id", ErrAttachmentMissing)
	}
	opener, ok := handler.(AttachmentOpener)
	if !ok {
		return nil, fmt.Errorf("%w: handler cannot open attachments", ErrAttachmentMissing)
	}
	data, err := opener.OpenAttachment(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	if err := op.CheckOwnership(ctx); err != nil {
		return nil, err
	}
	return data, nil
}

// ClassifyAttachmentErr maps attachment resolution failures for senders: a
// structurally missing reference is permanent, a store read failure is
// retryable, and an already-classified send error (e.g. the post-read lease
// check) passes through unchanged.
func ClassifyAttachmentErr(platform string, err error) error {
	var sendErr *SendError
	if errors.As(err, &sendErr) {
		return err
	}
	if errors.Is(err, ErrAttachmentMissing) {
		return SendErrorf(SendPermanent, "%s: %v", platform, err)
	}
	return SendErrorf(SendRetryable, "%s: %v", platform, err)
}

// AttachmentKinds for AttachmentOpPayload.Kind.
const (
	AttachmentImage = "image"
	AttachmentFile  = "file"
)

// CollectReplyEvents folds a completed turn's events into the deliverable
// body for adapters without a draft surface: final text (with the tool
// history rendered, matching the live stream's tail), plus attachments.
func CollectReplyEvents(events []Event) (string, []ImageEvent, []FileEvent) {
	var text strings.Builder
	var tracker ToolTracker
	var images []ImageEvent
	var files []FileEvent
	for _, evt := range events {
		switch {
		case evt.Image != nil:
			images = append(images, *evt.Image)
		case evt.File != nil:
			files = append(files, *evt.File)
		default:
			if evt.ToolUse != nil {
				tracker.Handle(evt.ToolUse)
			}
			text.WriteString(evt.Text)
		}
	}
	response := text.String()
	if tracker.HasHistory() {
		response += tracker.RenderFinal()
	}
	return response, images, files
}
