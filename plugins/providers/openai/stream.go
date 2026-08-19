package openai

import (
	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/ssestream"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// consumeStream reads SSE events from the OpenAI chat completion stream.
// It returns true if a finish_reason was received, meaning any subsequent
// sdkStream.Err() is a benign SDK parser artifact.
func consumeStream(sdkStream *ssestream.Stream[sdk.ChatCompletionChunk], out *providers.ChannelEventStream) bool {
	// Track tool_call_index → tool_call_id so argument deltas carry the correct ID.
	// OpenAI only sends tc.ID in the first chunk per tool call; subsequent chunks use Index only.
	indexToID := make(map[int]string)
	completed := false

	for sdkStream.Next() {
		chunk := sdkStream.Current()
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				completed = true
			}
		}
		for _, e := range mapChunk(chunk, indexToID) {
			out.Emit(e)
		}
	}
	return completed
}

func mapChunk(chunk sdk.ChatCompletionChunk, indexToID map[int]string) []ai.AssistantEvent {
	var events []ai.AssistantEvent

	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if delta.Content != "" {
			events = append(events, ai.EventTextDelta{Text: delta.Content})
		}
		for _, tc := range delta.ToolCalls {
			idx := int(tc.Index)
			if tc.ID != "" {
				indexToID[idx] = tc.ID
			}
			events = append(events, ai.EventToolCallDelta{
				ID:        indexToID[idx],
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		if choice.FinishReason != "" {
			events = append(events, ai.EventStop{Reason: mapStopReason(choice.FinishReason)})
		}
	}

	if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 || chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
		// prompt_tokens includes the cached tokens; ai.Usage keeps the two
		// disjoint so each is priced at its own rate.
		events = append(events, ai.EventUsage{Usage: ai.UsageWithCachedInput(
			int(chunk.Usage.PromptTokens),
			int(chunk.Usage.CompletionTokens),
			int(chunk.Usage.PromptTokensDetails.CachedTokens),
			int(chunk.Usage.TotalTokens),
		)})
	}

	return events
}

func mapStopReason(reason string) ai.StopReason {
	switch reason {
	case "stop":
		return ai.StopReasonStop
	case "length":
		return ai.StopReasonLength
	case "tool_calls":
		return ai.StopReasonToolUse
	default:
		return ai.StopReasonUnknown
	}
}
