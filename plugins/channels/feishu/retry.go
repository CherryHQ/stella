package feishu

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

const feishuSendAttempts = 3

var feishuSendRetryDelays = [...]time.Duration{200 * time.Millisecond, 800 * time.Millisecond}

type feishuAPIError struct {
	code int
	msg  string
}

func (e *feishuAPIError) Error() string {
	return fmt.Sprintf("code=%d msg=%s", e.code, e.msg)
}

func isTransientFeishuError(err error) bool {
	if err == nil || errors.Is(err, errCardContentBuild) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}
	var apiErr *feishuAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	// Feishu documents these codes for throttling and transient service failure.
	// All other API errors (including card validation) fail fast.
	switch apiErr.code {
	case 99991400, 99991661, 99991663:
		return true
	default:
		return apiErr.code >= 500 && apiErr.code < 600
	}
}

func (b *Bot) retryFeishuSend(ctx context.Context, operation string, send func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := range feishuSendAttempts {
		attemptCtx, cancel := context.WithTimeout(ctx, feishuAPITimeout)
		err := send(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientFeishuError(err) || attempt == feishuSendAttempts-1 {
			return err
		}
		delay := feishuSendRetryDelays[attempt]
		logger().Warn("transient Feishu message delivery failure; retrying", "operation", operation, "attempt", attempt+1, "delay", delay, "error", err)
		if err := b.waitForFeishuRetry(ctx, delay); err != nil {
			return errors.Join(lastErr, err)
		}
	}
	return lastErr
}

func (b *Bot) waitForFeishuRetry(ctx context.Context, delay time.Duration) error {
	if b.retryPauseFn != nil {
		return b.retryPauseFn(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
