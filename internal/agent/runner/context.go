package runner

import "context"

type systemOverrideKey struct{}

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
