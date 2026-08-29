package tools

import (
	"context"
	"net/http"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
)

type parentImageCapabilityKey struct{}

// WithParentImageCapability records the effective model's image capability for
// the current tool execution. The loop injects this after pre-LLM hooks run so
// routing and provider projection use the same model fact.
func WithParentImageCapability(ctx context.Context, capability ai.ImageCapability) context.Context {
	return context.WithValue(ctx, parentImageCapabilityKey{}, capability)
}

// ParentImageCapabilityFromContext fails closed when no effective model fact was
// injected. Unknown therefore takes the textual route rather than risking a
// pixel result that the provider will silently downgrade.
func ParentImageCapabilityFromContext(ctx context.Context) ai.ImageCapability {
	capability, _ := ctx.Value(parentImageCapabilityKey{}).(ai.ImageCapability)
	return capability
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
