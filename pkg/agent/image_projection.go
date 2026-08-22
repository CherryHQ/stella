package agent

import (
	"context"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
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
	// Budget only after projection: these are the exact blocks providers flatten,
	// including baseline text and inter-block separators. Canonical history stays
	// lossless while every replay gets the same bounded provider-visible copy.
	applyProjectedToolOutputBudgets(out)
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

func applyProjectedToolOutputBudgets(messages []ai.Message) {
	for start := 0; start < len(messages); {
		if _, ok := messages[start].(ai.ToolResultMessage); !ok {
			start++
			continue
		}

		end := start
		var outputs []string
		for end < len(messages) {
			result, ok := messages[end].(ai.ToolResultMessage)
			if !ok {
				break
			}
			outputs = append(outputs, ai.FlattenText(result.Content))
			end++
		}
		for i, budgeted := range pkgtools.ApplyTurnOutputBudget(outputs) {
			if !budgeted.Truncated {
				continue
			}
			result := messages[start+i].(ai.ToolResultMessage)
			result.Content = applyProjectedTextBudget(result.Content, budgeted)
			messages[start+i] = result
		}
		start = end
	}
}

func applyProjectedTextBudget(blocks []ai.ContentBlock, budget pkgtools.TurnOutputBudgetResult) []ai.ContentBlock {
	out := make([]ai.ContentBlock, len(blocks))
	copy(out, blocks)

	textIndexes := make([]int, 0, len(blocks))
	for i, block := range blocks {
		if text, ok := block.(ai.TextContent); ok && text.Text != "" {
			textIndexes = append(textIndexes, i)
		}
	}
	if len(textIndexes) == 0 {
		return out
	}

	headParts := make(map[int]string, len(textIndexes))
	headRemaining := budget.Head
	for _, i := range textIndexes {
		if headRemaining == "" {
			break
		}
		text := blocks[i].(ai.TextContent).Text
		if len(headRemaining) < len(text) {
			headParts[i] = headRemaining
			headRemaining = ""
			break
		}
		headParts[i] = text
		headRemaining = strings.TrimPrefix(headRemaining[len(text):], " ")
	}

	tailParts := make(map[int]string, len(textIndexes))
	tailRemaining := budget.Tail
	for pos := len(textIndexes) - 1; pos >= 0 && tailRemaining != ""; pos-- {
		i := textIndexes[pos]
		text := blocks[i].(ai.TextContent).Text
		if len(tailRemaining) < len(text) {
			tailParts[i] = tailRemaining
			tailRemaining = ""
			break
		}
		tailParts[i] = text
		tailRemaining = strings.TrimSuffix(tailRemaining[:len(tailRemaining)-len(text)], " ")
	}

	for _, i := range textIndexes {
		out[i] = ai.TextContent{Text: headParts[i] + tailParts[i]}
	}
	if len(headParts) > 0 {
		lastHead := textIndexes[0]
		for _, i := range textIndexes {
			if headParts[i] != "" {
				lastHead = i
			}
		}
		text := out[lastHead].(ai.TextContent)
		text.Text = headParts[lastHead] + budget.Marker + tailParts[lastHead]
		out[lastHead] = text
		return out
	}
	if len(tailParts) > 0 {
		for _, i := range textIndexes {
			if tailParts[i] == "" {
				continue
			}
			text := out[i].(ai.TextContent)
			text.Text = budget.Marker + tailParts[i]
			out[i] = text
			return out
		}
	}
	out[textIndexes[0]] = ai.TextContent{Text: budget.Marker}
	return out
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
