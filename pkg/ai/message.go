package ai

import (
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// TextSignatureV1 carries model-generated text signature metadata.
type TextSignatureV1 struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	Phase string `json:"phase,omitempty"` // "commentary" | "final_answer"
}

// ContentBlock is the normalized assistant content unit.
type ContentBlock interface {
	contentBlockKind() string
}

// TextContent represents plain text output.
type TextContent struct {
	Text          string
	TextSignature string // legacy id string or TextSignatureV1 JSON
}

func (TextContent) contentBlockKind() string { return "text" }

// ThinkingContent stores provider reasoning metadata.
type ThinkingContent struct {
	Thinking  string
	Signature string
	Redacted  bool
}

func (ThinkingContent) contentBlockKind() string { return "thinking" }

const (
	// MaxImageInputBytes is the largest original image accepted for canonical
	// session ingestion before it is converted to a provider payload.
	MaxImageInputBytes = 30 * 1024 * 1024
	// MaxImagesPerMessage and MaxAggregateImageBytes bound one image-bearing
	// message before decoding or persistence allocates its originals.
	MaxImagesPerMessage    = 8
	MaxAggregateImageBytes = 60 * 1024 * 1024
)

// ImageContent represents ephemeral, base64-encoded provider or tool input.
// It must not be written to new ordinary-session history.
type ImageContent struct {
	Data     string // base64 encoded
	MimeType string // e.g. "image/jpeg", "image/png"
}

func (ImageContent) contentBlockKind() string { return "image" }

// UnavailableImageProjection is deliberately independent of a renderer,
// timestamp, and error body. It is the only durable text for an image whose
// original bytes persisted but whose baseline could not be produced.
const UnavailableImageProjection = "[Image baseline unavailable.]"

// ImageBaseline is the immutable provider projection for one session image.
// The zero value is the stable unavailable marker; ready values use the sole
// V1 text contract validated by Validate.
type ImageBaseline struct {
	Text string
}

func (b ImageBaseline) Validate() error {
	if b.Text == "" {
		return nil
	}
	return ValidateImageBaselineText(b.Text)
}

// Projection returns the bounded stable text used by durable history.
func (b ImageBaseline) Projection() string {
	if b.Text == "" {
		return UnavailableImageProjection
	}
	return b.Text
}

// ParseImageBaseline reconstructs a baseline from its exact durable projection.
func ParseImageBaseline(projection string) (ImageBaseline, error) {
	if projection == UnavailableImageProjection {
		return ImageBaseline{}, nil
	}
	baseline := ImageBaseline{Text: projection}
	if err := baseline.Validate(); err != nil {
		return ImageBaseline{}, err
	}
	return baseline, nil
}

// ImageRefContent is a canonical reference to immutable session media. MIME is
// owned by the media row and resolved only during authorized hydration.
type ImageRefContent struct {
	MediaID  string
	Baseline ImageBaseline
}

func (ImageRefContent) contentBlockKind() string { return "image_ref" }

// Validate checks the storage invariants for a canonical image reference.
func (r ImageRefContent) Validate() error {
	if strings.TrimSpace(r.MediaID) == "" {
		return fmt.Errorf("image ref requires media ID")
	}
	if err := r.Baseline.Validate(); err != nil {
		return fmt.Errorf("image ref baseline: %w", err)
	}
	return nil
}

// DataURI returns the image as a data URI string (e.g. "data:image/jpeg;base64,...").
func (ic ImageContent) DataURI() string {
	return "data:" + ic.MimeType + ";base64," + ic.Data
}

// ToolCall represents a tool invocation emitted by an assistant.
type ToolCall struct {
	ID               string
	Name             string
	Arguments        map[string]any
	ThoughtSignature string
}

func (ToolCall) contentBlockKind() string { return "toolCall" }

// Message is the base conversation entry.
type Message interface {
	messageRole() string
}

// UserMessage contains user-provided content.
// Content is string or []ContentBlock (TextContent | ImageContent | ImageRefContent).
type UserMessage struct {
	Content   any
	Timestamp time.Time
}

func (UserMessage) messageRole() string { return "user" }

// TimestampedContent returns Content with a "ts:<unix>\n" prefix when Timestamp is set.
func (m UserMessage) TimestampedContent() any {
	if m.Timestamp.IsZero() {
		return m.Content
	}
	prefix := fmt.Sprintf("ts:%d\n", m.Timestamp.Unix())
	switch c := m.Content.(type) {
	case string:
		return prefix + c
	case []ContentBlock:
		if len(c) == 0 {
			return []ContentBlock{TextContent{Text: strings.TrimSuffix(prefix, "\n")}}
		}
		out := make([]ContentBlock, len(c))
		copy(out, c)
		if t, ok := out[0].(TextContent); ok {
			t.Text = prefix + t.Text
			out[0] = t
		} else {
			out = append([]ContentBlock{TextContent{Text: strings.TrimSuffix(prefix, "\n")}}, out...)
		}
		return out
	default:
		return prefix + fmt.Sprintf("%v", m.Content)
	}
}

// AssistantMessage contains assistant output and metadata.
type AssistantMessage struct {
	Content      []ContentBlock
	Api          string
	Provider     string
	Model        string
	Usage        Usage
	StopReason   StopReason
	ErrorMessage string
	Timestamp    time.Time
}

func (AssistantMessage) messageRole() string { return "assistant" }

// ToolResultMessage links a tool response to a tool call.
type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []ContentBlock // TextContent | ImageContent | ImageRefContent
	Details    any
	IsError    bool
	Timestamp  time.Time
	References []renderrefs.Reference
}

func (ToolResultMessage) messageRole() string { return "tool" }

// HasImage reports whether the given content blocks contain at least one ImageContent.
func HasImage(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if _, ok := b.(ImageContent); ok {
			return true
		}
	}
	return false
}

// HasImageRef reports whether content contains durable session media.
func HasImageRef(blocks []ContentBlock) bool {
	for _, block := range blocks {
		if _, ok := block.(ImageRefContent); ok {
			return true
		}
	}
	return false
}

// FlattenText extracts and joins all TextContent values from content blocks.
func FlattenText(blocks []ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if t, ok := block.(TextContent); ok && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}
