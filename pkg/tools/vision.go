package tools

import (
	"context"
	"net/http"
	"strings"
)

type imageResultModeKey struct{}

// ImageResultMode tells image-producing tools which storage boundary will own
// their result. It is one explicit state, not two independently configurable
// booleans.
type ImageResultMode uint8

const (
	// ImageResultLegacy preserves the old inline group codec and is the default.
	ImageResultLegacy ImageResultMode = iota
	ImageResultCanonical
)

func WithImageResultMode(ctx context.Context, mode ImageResultMode) context.Context {
	return context.WithValue(ctx, imageResultModeKey{}, mode)
}

func ImageResultModeFromContext(ctx context.Context) ImageResultMode {
	mode, _ := ctx.Value(imageResultModeKey{}).(ImageResultMode)
	return mode
}

// DetectImageMime returns the canonical MIME type for image bytes the tool layer
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
