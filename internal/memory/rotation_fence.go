package memory

import "context"

// RotationFence is the immutable durable FIFO coordinate accepted with a
// channel /new command. PostgreSQL validates it in the same transaction as the
// Session compare-and-rotate so neither a historical revision nor a stale
// claim can rotate the currently bound Session.
type RotationFence struct {
	FIFOID            string
	ChannelID         string
	BindingKey        string
	BindingRevision   int64
	ExpectedSessionID string
	ClaimToken        string
}

type rotationFenceContextKey struct{}

func WithRotationFence(ctx context.Context, fence RotationFence) context.Context {
	return context.WithValue(ctx, rotationFenceContextKey{}, fence)
}

func RotationFenceFromContext(ctx context.Context) (RotationFence, bool) {
	fence, ok := ctx.Value(rotationFenceContextKey{}).(RotationFence)
	return fence, ok
}
