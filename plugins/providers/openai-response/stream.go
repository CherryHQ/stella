package openairesponse

import (
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/responses"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// consumeStream reads SSE events from the OpenAI Responses stream.
// It returns true if the stream reached a terminal event (completed/failed/incomplete),
// meaning any subsequent sdkStream.Err() is a benign SDK parser artifact.
func consumeStream(sdkStream *ssestream.Stream[responses.ResponseStreamEventUnion], out *providers.ChannelEventStream) bool {
	// Track item_id → call_id so argument deltas use the correct call ID.
	itemToCall := make(map[string]string)
	completed := false

	for sdkStream.Next() {
		event := sdkStream.Current()
		switch event.Type {
		case "response.completed", "response.failed", "response.incomplete":
			completed = true
		}
		for _, e := range mapEvent(event, itemToCall) {
			out.Emit(e)
		}
	}
	return completed
}

func mapEvent(event responses.ResponseStreamEventUnion, itemToCall map[string]string) []ai.AssistantEvent {
	var events []ai.AssistantEvent

	switch event.Type {
	case "response.output_text.delta":
		if event.Delta.OfString != "" {
			events = append(events, ai.EventTextDelta{Text: event.Delta.OfString})
		}

	case "response.output_item.added":
		if event.Item.Type == "function_call" {
			// Record item_id → call_id so argument deltas can resolve the call ID.
			itemToCall[event.Item.ID] = event.Item.CallID
			events = append(events, ai.EventToolCallDelta{
				ID:   event.Item.CallID,
				Name: event.Item.Name,
			})
		}

	case "response.function_call_arguments.delta":
		callID := event.ItemID
		if mapped, ok := itemToCall[event.ItemID]; ok {
			callID = mapped
		}
		events = append(events, ai.EventToolCallDelta{
			ID:        callID,
			Arguments: event.Delta.OfString,
		})

	case "response.function_call_arguments.done":
		// Arguments fully accumulated via deltas; nothing extra needed.

	case "response.completed":
		usage := event.Response.Usage
		if usage.TotalTokens > 0 || usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.InputTokensDetails.CachedTokens > 0 {
			// input_tokens includes cached_tokens; ai.Usage keeps the two
			// disjoint so each is priced at its own rate.
			events = append(events, ai.EventUsage{Usage: ai.UsageWithCachedInput(
				int(usage.InputTokens),
				int(usage.OutputTokens),
				int(usage.InputTokensDetails.CachedTokens),
				int(usage.TotalTokens),
			)})
		}
		events = append(events, ai.EventStop{Reason: mapStopReason(event.Response.Status)})

	case "response.failed":
		events = append(events, ai.EventStop{Reason: ai.StopReasonError})

	case "response.incomplete":
		events = append(events, ai.EventStop{Reason: ai.StopReasonLength})
	}

	return events
}

func mapStopReason(status responses.ResponseStatus) ai.StopReason {
	switch status {
	case responses.ResponseStatusCompleted:
		return ai.StopReasonStop
	case responses.ResponseStatusIncomplete:
		return ai.StopReasonLength
	case responses.ResponseStatusFailed:
		return ai.StopReasonError
	default:
		return ai.StopReasonUnknown
	}
}
