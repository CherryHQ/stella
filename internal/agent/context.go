package agent

import (
	"context"

	"github.com/CherryHQ/stella/internal/core/agentctx"
)

// WithSystemOverride returns a child context that carries a per-run system prompt override.
func WithSystemOverride(ctx context.Context, system string) context.Context {
	return agentctx.WithSystemOverride(ctx, system)
}

// SystemOverrideFromContext returns the per-run system prompt override when present.
func SystemOverrideFromContext(ctx context.Context) (string, bool) {
	return agentctx.SystemOverrideFromContext(ctx)
}

// WithChannel returns a child context that carries the current chat channel.
func WithChannel(ctx context.Context, channel string) context.Context {
	return agentctx.WithChannel(ctx, channel)
}

// ChannelFromContext returns the current chat channel when present.
func ChannelFromContext(ctx context.Context) (string, bool) {
	return agentctx.ChannelFromContext(ctx)
}

// WithExcludedTools returns a child context that hides the named tools for a single run.
func WithExcludedTools(ctx context.Context, names ...string) context.Context {
	return agentctx.WithExcludedTools(ctx, names...)
}

// ExcludedToolsFromContext returns the per-run excluded tool names when present.
func ExcludedToolsFromContext(ctx context.Context) []string {
	return agentctx.ExcludedToolsFromContext(ctx)
}
