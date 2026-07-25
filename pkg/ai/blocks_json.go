package ai

import (
	"encoding/json"
	"fmt"
)

// ContentBlockJSON is the canonical storage serialization for user-visible
// content blocks (text and image). The wire shape matches the LCM message
// store so serialized blocks are interchangeable across stores.
type ContentBlockJSON struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// MarshalContentBlocks serializes text and image blocks to the canonical JSON
// array. Other block kinds (thinking, tool calls) are storage-internal and are
// skipped.
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

// UnmarshalContentBlocks is the inverse of MarshalContentBlocks. It returns
// nil for an empty array (or empty input), letting callers fall back to a
// plain-text path. Unknown kinds are skipped so old readers tolerate newer
// payloads.
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
