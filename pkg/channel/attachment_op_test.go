package channel

import (
	"context"
	"errors"
	"testing"
)

// stubAttachmentHandler satisfies Handler plus AttachmentOpener so tests can
// drive OpenAttachmentOp without a database.
type stubAttachmentHandler struct {
	open func(ctx context.Context, outboxID string) ([]byte, error)
}

func (stubAttachmentHandler) HandleIncoming(context.Context, IncomingMessage, string, string) (string, bool, *ChatStream, error) {
	return "", false, nil, nil
}

func (h stubAttachmentHandler) OpenAttachment(ctx context.Context, outboxID string) ([]byte, error) {
	return h.open(ctx, outboxID)
}

// A lease lost while the attachment read was in flight must stop the op
// before the bytes return: the sender would otherwise issue its platform call
// as a fenced-out replica.
func TestOpenAttachmentOpRechecksOwnershipAfterRead(t *testing.T) {
	ctx := context.Background()
	leaseHeld := true
	handler := stubAttachmentHandler{open: func(_ context.Context, id string) ([]byte, error) {
		if id != "op-1" {
			t.Fatalf("opened %q, want op-1", id)
		}
		leaseHeld = false // the store read outlived the lease
		return []byte("data"), nil
	}}
	op := OutboundOp{ID: "op-1", Guard: func(context.Context) error {
		if !leaseHeld {
			return errors.New("channel lease lost")
		}
		return nil
	}}

	data, err := OpenAttachmentOp(ctx, handler, op)
	if data != nil {
		t.Fatalf("returned bytes after lease loss")
	}
	var sendErr *SendError
	if !errors.As(err, &sendErr) || sendErr.Class != SendRetryable {
		t.Fatalf("err = %v, want retryable SendError", err)
	}
}

// While the lease holds, the resolved bytes reach the sender.
func TestOpenAttachmentOpReturnsBytesWhileOwned(t *testing.T) {
	handler := stubAttachmentHandler{open: func(context.Context, string) ([]byte, error) {
		return []byte("stored-bytes"), nil
	}}
	data, err := OpenAttachmentOp(context.Background(), handler, OutboundOp{ID: "op-1"})
	if err != nil {
		t.Fatalf("OpenAttachmentOp: %v", err)
	}
	if string(data) != "stored-bytes" {
		t.Fatalf("data = %q", data)
	}
}

// An op with no row id and a handler without the opener capability are both
// structurally permanent — no amount of retrying conjures the bytes.
func TestOpenAttachmentOpMissingReferenceIsPermanent(t *testing.T) {
	ctx := context.Background()
	handler := stubAttachmentHandler{open: func(context.Context, string) ([]byte, error) {
		return nil, errors.New("should not be called")
	}}
	if _, err := OpenAttachmentOp(ctx, handler, OutboundOp{}); !errors.Is(err, ErrAttachmentMissing) {
		t.Fatalf("no-id op err = %v, want ErrAttachmentMissing", err)
	}
	if _, err := OpenAttachmentOp(ctx, stubNoOpener{}, OutboundOp{ID: "op-1"}); !errors.Is(err, ErrAttachmentMissing) {
		t.Fatalf("non-opener handler err = %v, want ErrAttachmentMissing", err)
	}
}

type stubNoOpener struct{}

func (stubNoOpener) HandleIncoming(context.Context, IncomingMessage, string, string) (string, bool, *ChatStream, error) {
	return "", false, nil, nil
}
