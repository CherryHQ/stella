package feishu

import (
	"context"
	"fmt"
)

type feishuAPIError struct {
	code int
	msg  string
}

func (e *feishuAPIError) Error() string {
	return fmt.Sprintf("code=%d msg=%s", e.code, e.msg)
}

// retryFeishuSend retains its established call-site name, but deliberately
// performs exactly one request. Timeout, transport, 5xx, and throttling errors
// can all be returned after Feishu accepted the mutation, so retrying would
// duplicate an outcome-unknown outbound effect.
func (b *Bot) retryFeishuSend(ctx context.Context, _ string, send func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	attemptCtx, cancel := context.WithTimeout(ctx, feishuAPITimeout)
	defer cancel()
	return send(attemptCtx)
}
