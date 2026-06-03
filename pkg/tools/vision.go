package tools

import "context"

type visionKey struct{}

// WithVision records whether the active model can accept image input, so tools
// (e.g. read) can decide whether to return images inline or fall back to text.
func WithVision(ctx context.Context, supported bool) context.Context {
	return context.WithValue(ctx, visionKey{}, supported)
}

// VisionFromContext reports whether the active model accepts images. It defaults
// to true (fail-open) when unset, so image-capable behavior is the default and
// only models explicitly known to lack vision trigger text fallbacks.
func VisionFromContext(ctx context.Context) bool {
	v, ok := ctx.Value(visionKey{}).(bool)
	if !ok {
		return true
	}
	return v
}
