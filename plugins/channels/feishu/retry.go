package feishu

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

const feishuSendAttempts = 3

const maxFeishuRetryAfter = 2 * time.Second

var feishuSendRetryDelays = [...]time.Duration{200 * time.Millisecond, 800 * time.Millisecond}

type feishuAPIError struct {
	code       int
	msg        string
	retryAfter time.Duration
}

func (e *feishuAPIError) Error() string {
	return fmt.Sprintf("code=%d msg=%s", e.code, e.msg)
}

type deliveryOutcome string

const (
	deliveryNotSent deliveryOutcome = "not_sent"
	deliveryUnknown deliveryOutcome = "unknown"
)

type feishuDeliveryError struct {
	operation string
	outcome   deliveryOutcome
	err       error
}

func (e *feishuDeliveryError) Error() string {
	return fmt.Sprintf("%s: %s outcome: %v", e.operation, e.outcome, e.err)
}

func (e *feishuDeliveryError) Unwrap() error { return e.err }

func newFeishuAPIError(code int, msg string, header http.Header) *feishuAPIError {
	return &feishuAPIError{code: code, msg: msg, retryAfter: parseRetryAfter(header.Get("Retry-After"))}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	seconds, err := strconv.ParseFloat(value, 64)
	if err == nil && seconds >= 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
		return min(time.Duration(seconds*float64(time.Second)), maxFeishuRetryAfter)
	}
	deadline, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	return min(max(deadline.Sub(nowFunc()), 0), maxFeishuRetryAfter)
}

func outcomeForFeishuError(err error) deliveryOutcome {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isLarkTransportError(err) {
		return deliveryUnknown
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return deliveryUnknown
	}
	return deliveryNotSent
}

func isLarkTransportError(err error) bool {
	var clientTimeout *larkcore.ClientTimeoutError
	var serverTimeout *larkcore.ServerTimeoutError
	var dialFailed *larkcore.DialFailedError
	return errors.As(err, &clientTimeout) || errors.As(err, &serverTimeout) || errors.As(err, &dialFailed)
}

func isTransientFeishuError(err error) bool {
	if err == nil || errors.Is(err, errCardContentBuild) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if isLarkTransportError(err) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
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
			return &feishuDeliveryError{operation: operation, outcome: outcomeForFeishuError(err), err: err}
		}
		delay := feishuSendRetryDelays[attempt]
		var apiErr *feishuAPIError
		if errors.As(err, &apiErr) && apiErr.retryAfter > delay {
			delay = apiErr.retryAfter
		}
		logger().Warn("transient Feishu message delivery failure; retrying", "operation", operation, "attempt", attempt+1, "delay", delay, "error", err)
		if err := b.waitForFeishuRetry(ctx, delay); err != nil {
			joined := errors.Join(lastErr, err)
			return &feishuDeliveryError{operation: operation, outcome: outcomeForFeishuError(joined), err: joined}
		}
	}
	return &feishuDeliveryError{operation: operation, outcome: outcomeForFeishuError(lastErr), err: lastErr}
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
