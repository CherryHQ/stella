package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// OutboundAddress fixes where one durable outbound operation lands: the
// physical platform chat coordinates plus the reply anchor, frozen at
// enqueue time. Senders replay it verbatim; they never re-resolve identity.
type OutboundAddress struct {
	ChatKey    string `json:"chat_key"`
	ThreadKey  string `json:"thread_key,omitempty"`
	ReplyToKey string `json:"reply_to_key,omitempty"`
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
