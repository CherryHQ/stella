package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrRawImageContent           = errors.New("raw image content cannot be stored canonically")
	ErrUnsupportedCanonicalBlock = errors.New("content block cannot be stored canonically")
)

// ContentBlockJSON is the compatibility serialization for deferred group
// history. Canonical ordinary-session references live only in message parts.
type ContentBlockJSON struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// MarshalContentBlocks serializes text and legacy inline image blocks. It
// remains compatible with group history while ordinary session history moves to
// the explicit canonical codec below.
func MarshalContentBlocks(blocks []ContentBlock) ([]byte, error) {
	out := make([]ContentBlockJSON, 0, len(blocks))
	for _, b := range blocks {
		switch b := b.(type) {
		case TextContent:
			out = append(out, ContentBlockJSON{Kind: "text", Text: b.Text})
		case ImageContent:
			out = append(out, ContentBlockJSON{Kind: "image", Data: b.Data, MimeType: b.MimeType})
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal content blocks: %w", err)
	}
	return data, nil
}

// ValidateCanonicalContentBlocks rejects provider-ready bytes and block kinds
// that cannot be represented by durable message parts.
func ValidateCanonicalContentBlocks(blocks []ContentBlock) error {
	for _, block := range blocks {
		switch block := block.(type) {
		case TextContent:
		case ImageRefContent:
			if err := block.Validate(); err != nil {
				return err
			}
		case ImageContent:
			return ErrRawImageContent
		default:
			return fmt.Errorf("%w: %T", ErrUnsupportedCanonicalBlock, block)
		}
	}
	return nil
}

// UnmarshalContentBlocks is the compatibility decoder. Legacy inline images
// remain ImageContent, while image_ref records retain their canonical type.
// Unknown kinds are skipped so old readers tolerate newer payloads.
func UnmarshalContentBlocks(data []byte) ([]ContentBlock, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var raw []ContentBlockJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal content blocks: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	blocks := make([]ContentBlock, 0, len(raw))
	for _, b := range raw {
		switch b.Kind {
		case "text":
			blocks = append(blocks, TextContent{Text: b.Text})
		case "image":
			blocks = append(blocks, ImageContent{Data: b.Data, MimeType: b.MimeType})
		}
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	return blocks, nil
}

// CloneContentBlocks returns a storage-safe copy. Content values are immutable
// except ToolCall.Arguments, which may contain nested maps or slices owned by a
// caller that mutates them after an asynchronous handoff.
func CloneContentBlocks(blocks []ContentBlock) []ContentBlock {
	out := make([]ContentBlock, len(blocks))
	for i, block := range blocks {
		switch b := block.(type) {
		case ToolCall:
			b.Arguments = cloneArguments(b.Arguments)
			out[i] = b
		default:
			out[i] = b
		}
	}
	return out
}

func cloneArguments(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneContentValue(value)
	}
	return out
}

func cloneContentValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneArguments(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneContentValue(item)
		}
		return out
	default:
		return value
	}
}

// FlattenCanonicalText is the stable text projection for durable blocks. It
// never exposes provider-ready bytes and gives unavailable images one fixed
// marker so compaction and storage do not acquire backend-specific errors.
func FlattenCanonicalText(blocks []ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case ImageRefContent:
			parts = append(parts, b.Baseline.Projection())
		}
	}
	return strings.Join(parts, " ")
}
