package tools

import (
	"context"
	"net/http"
	"strings"
)

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

// DetectImageMime returns the canonical MIME type for image bytes the read tool
// can present to a model (png, jpeg, gif, webp), or "" for anything else.
func DetectImageMime(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct
	}
	return ""
}
