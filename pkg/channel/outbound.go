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
	// SourceAccountKey is the bot account that received the triggering event;
	// an adapter whose credentials changed must not send it (account_mismatch).
	SourceAccountKey string
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

// GroupReplyOpPayload is the frozen body of the "send_group_reply" outbox op —
// everything a GroupPublisher needs to replay an accepted group reply on the
// replica that owns the channel lease.
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
}

// ReplyOpPayload is the frozen body of the "send_reply" outbox op — the whole
// recorded turn event list, so the owning adapter can replay it through its
// draft/edit machinery (or flatten it to text plus attachments) on whichever
// replica holds the channel lease.
type ReplyOpPayload struct {
	V         int     `json:"v"`
	SessionID string  `json:"session_id,omitempty"`
	Events    []Event `json:"events"`
}

// ReplayStream rebuilds a completed turn as a ChatStream. Abort cannot cross
// a process boundary: the turn has already ended when the op dispatches.
func (p ReplyOpPayload) ReplayStream() *ChatStream {
	events := make(chan Event, len(p.Events))
	for _, evt := range p.Events {
		events <- evt
	}
	close(events)
	return &ChatStream{Events: events, SessionID: p.SessionID}
}

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

// ReplayChatStream rebuilds a GroupPublishRequest stream from a persisted op.
// Abort cannot cross a process boundary, so the reconstructed affordance is a
// no-op — publish happens after the turn ends regardless.
func (p GroupReplyOpPayload) PublishRequest() GroupPublishRequest {
	events := make(chan Event, len(p.Events))
	for _, evt := range p.Events {
		events <- evt
	}
	close(events)
	return GroupPublishRequest{
		Platform:         p.Platform,
		PlatformGroupID:  p.PlatformGroupID,
		PlatformThreadID: p.PlatformThreadID,
		ReplyTo:          p.ReplyTo,
		Stream:           &ChatStream{Events: events, SessionID: p.SessionID},
		DeliveryID:       p.DeliveryID,
		RequesterID:      p.RequesterID,
		Abort:            func() bool { return false },
	}
}
