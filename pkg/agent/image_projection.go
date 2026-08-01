package agent

import (
	"context"

	"github.com/CherryHQ/stella/pkg/ai"
)

// MediaLoader opens one immutable session image for the current user. The
// implementation owns authorization and integrity verification.
type MediaLoader func(context.Context, string) (ai.ImageContent, error)

// ProjectionError identifies a broken canonical/provider boundary. Its detail
// is intentionally private: callers can classify only the exported sentinels.
type ProjectionError struct{ reason string }

func (e *ProjectionError) Error() string { return "agent image projection: " + e.reason }

func projectionError(reason string) *ProjectionError { return &ProjectionError{reason: reason} }

var (
	ErrAssistantImageBlock = projectionError("assistant image block is invalid")
	ErrImageRefUnresolved  = projectionError("image reference reached provider boundary")
	ErrUnsupportedImage    = projectionError("unsupported model received image content")
)

// projectImages converts canonical references and legacy inline images into a
// provider-safe request before TransformMessages can insert synthetic blocks.
// activeStart is an index into the original transcript and remains constant for
// the whole Run, including progress nudges and tool-loop results.
func projectImages(ctx context.Context, cfg loopConfig, messages []ai.Message, activeStart int, memo map[string]ai.ImageContent) ([]ai.Message, error) {
	if activeStart < 0 || activeStart > len(messages) {
		return nil, projectionError("invalid active boundary")
	}
	out := make([]ai.Message, len(messages))
	for i, msg := range messages {
		projected, err := projectMessage(ctx, cfg, msg, i >= activeStart, memo)
		if err != nil {
			return nil, err
		}
		out[i] = projected
	}
	return out, nil
}

func projectMessage(ctx context.Context, cfg loopConfig, msg ai.Message, active bool, memo map[string]ai.ImageContent) (ai.Message, error) {
	switch m := msg.(type) {
	case ai.UserMessage:
		blocks, ok := m.Content.([]ai.ContentBlock)
		if !ok {
			return msg, nil
		}
		projected, err := projectBlocks(ctx, cfg, blocks, active, memo)
		if err != nil {
			return nil, err
		}
		m.Content = projected
		return m, nil
	case ai.ToolResultMessage:
		projected, err := projectBlocks(ctx, cfg, m.Content, active, memo)
		if err != nil {
			return nil, err
		}
		m.Content = projected
		return m, nil
	case ai.AssistantMessage:
		for _, block := range m.Content {
			switch block.(type) {
			case ai.ImageContent, ai.ImageRefContent:
				return nil, ErrAssistantImageBlock
			}
		}
		return msg, nil
	default:
		return msg, nil
	}
}

func projectBlocks(ctx context.Context, cfg loopConfig, blocks []ai.ContentBlock, active bool, memo map[string]ai.ImageContent) ([]ai.ContentBlock, error) {
	out := make([]ai.ContentBlock, len(blocks))
	for i, block := range blocks {
		switch b := block.(type) {
		case ai.ImageRefContent:
			if err := b.Validate(); err != nil {
				return nil, projectionError("invalid image reference")
			}
			if active && cfg.Model.ImageCapability() == ai.ImageSupported {
				image, ok := memo[b.MediaID]
				if !ok {
					if cfg.CanonicalImages == nil {
						return nil, projectionError("no media loader for active image")
					}
					loaded, err := cfg.CanonicalImages.Load(ctx, b.MediaID)
					if err != nil {
						out[i] = ai.TextContent{Text: b.Baseline.Projection()}
						continue
					}
					image = loaded
					memo[b.MediaID] = image
				}
				out[i] = image
				continue
			}
			out[i] = ai.TextContent{Text: b.Baseline.Projection()}
		case ai.ImageContent:
			if active && cfg.Model.ImageCapability() == ai.ImageSupported {
				out[i] = b
				continue
			}
			out[i] = ai.TextContent{Text: ai.UnavailableImageProjection}
		default:
			out[i] = block
		}
	}
	return out, nil
}

// validateNoImageRefs is deliberately immediately before every Stream: no
// adapter can receive a canonical reference.
func validateNoImageRefs(messages []ai.Message) error {
	for _, msg := range messages {
		for _, block := range messageBlocks(msg) {
			if _, ok := block.(ai.ImageRefContent); ok {
				return ErrImageRefUnresolved
			}
		}
	}
	return nil
}

// validateProviderImages adds the ordinary-session capability boundary: only
// declared image models may receive ephemeral provider image bytes.
func validateProviderImages(model ai.Model, messages []ai.Message) error {
	if err := validateNoImageRefs(messages); err != nil {
		return err
	}
	for _, msg := range messages {
		for _, block := range messageBlocks(msg) {
			if _, ok := block.(ai.ImageContent); ok && model.ImageCapability() != ai.ImageSupported {
				return ErrUnsupportedImage
			}
		}
	}
	return nil
}

func messageBlocks(msg ai.Message) []ai.ContentBlock {
	switch m := msg.(type) {
	case ai.UserMessage:
		blocks, _ := m.Content.([]ai.ContentBlock)
		return blocks
	case ai.ToolResultMessage:
		return m.Content
	case ai.AssistantMessage:
		return m.Content
	default:
		return nil
	}
}
