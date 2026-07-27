package agent

import (
	"context"

	"github.com/CherryHQ/stella/pkg/ai"
)

// ImageTextFunc renders one inline image as text for a model that cannot accept
// image input. index is the image's 1-based position in the request, so the
// rendering can refer to it.
//
// It returns text, never an error: one unreadable image must not abort a turn,
// so implementations report their own failure as the text they return.
type ImageTextFunc func(ctx context.Context, index int, img ai.ImageContent) string

// materializeImages replaces every inline image in the outgoing request with
// its text rendering, but only for a model whose configuration explicitly says
// it cannot accept images. Supported and undeclared models are left alone — the
// undeclared case fails open for the same reason tool-side vision does.
//
// The returned slice is safe to send: nothing the caller passed in is modified.
// history is shared with the session store, so both the message slice and each
// message's content slice are copied before a block is replaced. Persisted
// history keeps the original images; only this request sees the text.
func materializeImages(ctx context.Context, cfg loopConfig, messages []ai.Message) []ai.Message {
	if cfg.ImageText == nil || cfg.Model.ImageCapability() != ai.ImageUnsupported {
		return messages
	}

	out := messages
	copied := false
	index := 0
	for i, msg := range messages {
		replaced, changed := materializeMessage(ctx, cfg.ImageText, msg, &index)
		if !changed {
			continue
		}
		if !copied {
			out = make([]ai.Message, len(messages))
			copy(out, messages)
			copied = true
		}
		out[i] = replaced
	}
	return out
}

// materializeMessage renders the images in one message, returning the rewritten
// message and whether anything changed.
//
// Only user and tool-result messages are considered: those are the two kinds
// that carry images (channel attachments and the read tool respectively).
// Assistant messages are model output, which never contains image blocks.
func materializeMessage(ctx context.Context, render ImageTextFunc, msg ai.Message, index *int) (ai.Message, bool) {
	switch m := msg.(type) {
	case ai.UserMessage:
		blocks, ok := m.Content.([]ai.ContentBlock)
		if !ok {
			return msg, false
		}
		replaced, changed := materializeBlocks(ctx, render, blocks, index)
		if !changed {
			return msg, false
		}
		m.Content = replaced
		return m, true
	case ai.ToolResultMessage:
		replaced, changed := materializeBlocks(ctx, render, m.Content, index)
		if !changed {
			return msg, false
		}
		m.Content = replaced
		return m, true
	default:
		return msg, false
	}
}

func materializeBlocks(ctx context.Context, render ImageTextFunc, blocks []ai.ContentBlock, index *int) ([]ai.ContentBlock, bool) {
	if !ai.HasImage(blocks) {
		return blocks, false
	}
	out := make([]ai.ContentBlock, len(blocks))
	for i, block := range blocks {
		img, ok := block.(ai.ImageContent)
		if !ok {
			out[i] = block
			continue
		}
		*index++
		out[i] = ai.TextContent{Text: render(ctx, *index, img)}
	}
	return out, true
}
