package anthropic

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// consumeStream reads SSE events from the Anthropic message stream.
// It returns true if a message_stop event was received, meaning any subsequent
// sdkStream.Err() is a benign SDK parser artifact.
func consumeStream(sdkStream *ssestream.Stream[sdk.MessageStreamEventUnion], out *providers.ChannelEventStream) bool {
	// Track content_block_index → tool_call_id so argument deltas carry the correct ID.
	blockToID := make(map[int]string)
	completed := false

	for sdkStream.Next() {
		event := sdkStream.Current()
		if event.Type == "message_stop" {
			completed = true
		}
		for _, e := range mapEvent(event, blockToID) {
			out.Emit(e)
		}
	}
	return completed
}

func mapEvent(event sdk.MessageStreamEventUnion, blockToID map[int]string) []ai.AssistantEvent {
	switch event.Type {
	case "message_start":
		return []ai.AssistantEvent{ai.EventStart{}}

	case "content_block_start":
		cb := event.ContentBlock
		if cb.Type == "tool_use" {
			blockToID[int(event.Index)] = cb.ID
			return []ai.AssistantEvent{ai.EventToolCallDelta{ID: cb.ID, Name: cb.Name}}
		}
		return nil

	case "content_block_delta":
		delta := event.Delta
		var events []ai.AssistantEvent
		switch delta.Type {
		case "text_delta":
			if delta.Text != "" {
				events = append(events, ai.EventTextDelta{Text: delta.Text})
			}
		case "thinking_delta":
			if delta.Thinking != "" {
				events = append(events, ai.EventThinkingDelta{Thinking: delta.Thinking})
			}
		case "input_json_delta":
			if delta.PartialJSON != "" {
				events = append(events, ai.EventToolCallDelta{
					ID:        blockToID[int(event.Index)],
					Arguments: delta.PartialJSON,
				})
			}
		}
		return events

	case "message_delta":
		var events []ai.AssistantEvent
		if event.Delta.StopReason != "" {
			events = append(events, ai.EventStop{Reason: mapStopReason(string(event.Delta.StopReason))})
		}
		if event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0 || event.Usage.CacheReadInputTokens > 0 || event.Usage.CacheCreationInputTokens > 0 {
			events = append(events, ai.EventUsage{Usage: ai.Usage{
				InputTokens:  int(event.Usage.InputTokens),
				OutputTokens: int(event.Usage.OutputTokens),
				CacheRead:    int(event.Usage.CacheReadInputTokens),
				CacheWrite:   int(event.Usage.CacheCreationInputTokens),
				TotalTokens: int(event.Usage.InputTokens + event.Usage.OutputTokens +
					event.Usage.CacheReadInputTokens + event.Usage.CacheCreationInputTokens),
			}})
		}
		return events

	case "message_stop":
		return []ai.AssistantEvent{ai.EventDone{}}
	}

	return nil
}

func mapStopReason(reason string) ai.StopReason {
	switch reason {
	case "tool_use":
		return ai.StopReasonToolUse
	case "max_tokens":
		return ai.StopReasonLength
	case "end_turn":
		return ai.StopReasonStop
	default:
		return ai.StopReasonUnknown
	}
}
