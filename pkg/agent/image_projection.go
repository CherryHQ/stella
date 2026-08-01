package agent

import (
	"context"

	"github.com/CherryHQ/stella/pkg/ai"
)

// ImageTextFunc is the legacy request-only compatibility renderer for inline
// ImageContent read from old rows. New ordinary-session writes never use it.
type ImageTextFunc func(ctx context.Context, index int, img ai.ImageContent) string

// MediaLoader opens one immutable session image for the current user. The
// implementation owns authorization and integrity verification.
type MediaLoader func(context.Context, string) (ai.ImageContent, error)

// ProjectionStats contains aggregate, non-sensitive request projection facts.
type ProjectionStats struct {
	Capability          ai.ImageCapability
	ActivePixels        int
	ActivePixelBytes    int
	BaselineProjections int
	LegacyRendered      int
	Hydrations          int
}

// ProjectionObserver receives one aggregate observation per provider request.
type ProjectionObserver func(ProjectionStats)

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
func projectImages(ctx context.Context, cfg loopConfig, messages []ai.Message, activeStart int, memo map[string]ai.ImageContent) ([]ai.Message, ProjectionStats, error) {
	if !cfg.ImageProjection {
		return projectLegacyInlineHistory(ctx, cfg, messages), ProjectionStats{Capability: cfg.Model.ImageCapability()}, nil
	}
	if activeStart < 0 || activeStart > len(messages) {
		return nil, ProjectionStats{}, projectionError("invalid active boundary")
	}
	stats := ProjectionStats{Capability: cfg.Model.ImageCapability()}
	out := make([]ai.Message, len(messages))
	for i, msg := range messages {
		projected, err := projectMessage(ctx, cfg, msg, i >= activeStart, memo, &stats)
		if err != nil {
			return nil, stats, err
		}
		out[i] = projected
	}
	return out, stats, nil
}

func projectMessage(ctx context.Context, cfg loopConfig, msg ai.Message, active bool, memo map[string]ai.ImageContent, stats *ProjectionStats) (ai.Message, error) {
	var blocks []ai.ContentBlock
	switch m := msg.(type) {
	case ai.UserMessage:
		var ok bool
		blocks, ok = m.Content.([]ai.ContentBlock)
		if !ok {
			return msg, nil
		}
		projected, err := projectBlocks(ctx, cfg, blocks, active, memo, stats)
		if err != nil {
			return nil, err
		}
		m.Content = projected
		return m, nil
	case ai.ToolResultMessage:
		projected, err := projectBlocks(ctx, cfg, m.Content, active, memo, stats)
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

func projectBlocks(ctx context.Context, cfg loopConfig, blocks []ai.ContentBlock, active bool, memo map[string]ai.ImageContent, stats *ProjectionStats) ([]ai.ContentBlock, error) {
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
					if cfg.MediaLoader == nil {
						return nil, projectionError("no media loader for active image")
					}
					loaded, err := cfg.MediaLoader(ctx, b.MediaID)
					if err != nil {
						out[i] = ai.TextContent{Text: b.Baseline.Projection()}
						stats.BaselineProjections++
						continue
					}
					image = loaded
					memo[b.MediaID] = image
					stats.Hydrations++
				}
				out[i] = image
				stats.ActivePixels++
				stats.ActivePixelBytes += len(image.Data)
				continue
			}
			out[i] = ai.TextContent{Text: b.Baseline.Projection()}
			stats.BaselineProjections++
		case ai.ImageContent:
			if active && cfg.Model.ImageCapability() == ai.ImageSupported {
				out[i] = b
				stats.ActivePixels++
				stats.ActivePixelBytes += len(b.Data)
				continue
			}
			if cfg.LegacyImageText == nil {
				return nil, projectionError("legacy image has no compatibility renderer")
			}
			stats.LegacyRendered++
			out[i] = ai.TextContent{Text: cfg.LegacyImageText(ctx, stats.LegacyRendered, b)}
		default:
			out[i] = block
		}
	}
	return out, nil
}

// projectLegacyInlineHistory is the sole compatibility adapter for deferred
// group history. It preserves legacy inline rows: declared image models keep
// their inline pixels, while other models use LegacyImageText. Canonical ordinary
// sessions always use projectImages above and never request-time render refs.
func projectLegacyInlineHistory(ctx context.Context, cfg loopConfig, messages []ai.Message) []ai.Message {
	if cfg.LegacyImageText == nil || cfg.Model.ImageCapability() == ai.ImageSupported {
		return messages
	}
	out := make([]ai.Message, len(messages))
	stats := ProjectionStats{}
	for i, msg := range messages {
		projected, err := projectMessage(ctx, cfg, msg, false, nil, &stats)
		if err != nil {
			return messages
		}
		out[i] = projected
	}
	return out
}

// validateNoImageRefs is deliberately immediately before every Stream: no
// adapter can receive a canonical reference, including deferred group history.
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
