package runner

import "context"

type systemOverrideKey struct{}
type channelKey struct{}

// WithSystemOverride returns a child context that carries a per-run system prompt override.
func WithSystemOverride(ctx context.Context, system string) context.Context {
	if system == "" {
		return ctx
	}
	return context.WithValue(ctx, systemOverrideKey{}, system)
}

// SystemOverrideFromContext returns the per-run system prompt override when present.
func SystemOverrideFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	system, ok := ctx.Value(systemOverrideKey{}).(string)
	return system, ok && system != ""
}

// WithChannel returns a child context that carries the current chat channel.
func WithChannel(ctx context.Context, channel string) context.Context {
	if channel == "" {
		return ctx
	}
	return context.WithValue(ctx, channelKey{}, channel)
}

// ChannelFromContext returns the current chat channel when present.
func ChannelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	channel, ok := ctx.Value(channelKey{}).(string)
	return channel, ok && channel != ""
}
