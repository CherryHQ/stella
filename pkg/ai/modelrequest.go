package ai

import (
	"context"
	"sync/atomic"
)

// ModelRequest is the per-model-request scope carried in the context from the
// agent loop down to the HTTP transport. Provider SDKs retry internally, so
// the loop never sees how many network requests one logical call took; the
// transport is the only place that can count them, and this is how the count
// travels back up.
//
// It holds no telemetry types on purpose: the counter is domain state, the
// spans that consume it live in the transport (see pkg/httpclient).
type ModelRequest struct {
	Model string

	attempts atomic.Int64
}

// NextAttempt claims the next attempt number, starting at 1.
func (r *ModelRequest) NextAttempt() int { return int(r.attempts.Add(1)) }

// Attempts reports how many attempts have been claimed so far.
func (r *ModelRequest) Attempts() int { return int(r.attempts.Load()) }

type modelRequestKey struct{}

// WithModelRequest scopes ctx to one logical model request. Every HTTP attempt
// issued under the returned context, including provider-SDK retries, counts
// against the same ModelRequest.
func WithModelRequest(ctx context.Context, model string) (context.Context, *ModelRequest) {
	req := &ModelRequest{Model: model}
	return context.WithValue(ctx, modelRequestKey{}, req), req
}

// ModelRequestFrom returns the model request scope, or nil when ctx is not
// inside one (any HTTP call that is not a model request).
func ModelRequestFrom(ctx context.Context) *ModelRequest {
	req, _ := ctx.Value(modelRequestKey{}).(*ModelRequest)
	return req
}
