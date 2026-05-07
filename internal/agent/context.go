package agent

import "context"

type (
	systemOverrideKey struct{}
	channelKey        struct{}
	excludedToolsKey  struct{}
	groupContextKey   struct{}
)

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

// WithExcludedTools returns a child context that hides the named tools for a single run.
func WithExcludedTools(ctx context.Context, names ...string) context.Context {
	filtered := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		filtered = append(filtered, name)
	}
	if len(filtered) == 0 {
		return ctx
	}
	return context.WithValue(ctx, excludedToolsKey{}, filtered)
}

// WithGroupContext returns a child context carrying ephemeral group chat context
// to be appended to the system prompt. Not stored in memory.
func WithGroupContext(ctx context.Context, text string) context.Context {
	if text == "" {
		return ctx
	}
	return context.WithValue(ctx, groupContextKey{}, text)
}

// GroupContextFromCtx returns the group chat context when present.
func GroupContextFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(groupContextKey{}).(string)
	return s
}

// ExcludedToolsFromContext returns the per-run excluded tool names when present.
func ExcludedToolsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	names, _ := ctx.Value(excludedToolsKey{}).([]string)
	if len(names) == 0 {
		return nil
	}
	out := make([]string, len(names))
	copy(out, names)
	return out
}
