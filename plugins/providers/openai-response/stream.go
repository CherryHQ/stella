package openairesponse

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/responses"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// A clean HTTP EOF is not a completed response: the missing tail may contain
// the tool call following an otherwise plausible progress message.
func consumeStream(sdkStream *ssestream.Stream[responses.ResponseStreamEventUnion], out *providers.ChannelEventStream) error {
	state := newStreamState()
	for sdkStream.Next() {
		event := sdkStream.Current()
		events, err := state.mapEvent(event)
		if err != nil {
			return err
		}
		for _, e := range events {
			out.Emit(e)
		}
		switch event.Type {
		case "response.completed", "response.failed", "response.incomplete":
			// A terminal event is authoritative; do not parse proxy trailers.
			return nil
		}
	}
	if err := sdkStream.Err(); err != nil {
		return err
	}
	return errors.New("responses stream ended without a terminal event")
}

type streamedCall struct {
	name      string
	arguments string
}

type streamState struct {
	itemToCall map[string]string
	calls      map[string]streamedCall
	text       map[textPart]string
}

type textPart struct {
	itemID string
	index  int64
}

func newStreamState() *streamState {
	return &streamState{itemToCall: make(map[string]string), calls: make(map[string]streamedCall), text: make(map[textPart]string)}
}

func (s *streamState) mapEvent(event responses.ResponseStreamEventUnion) ([]ai.AssistantEvent, error) {
	var events []ai.AssistantEvent

	switch event.Type {
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta.OfString != "" {
			s.text[textPart{event.ItemID, event.ContentIndex}] += event.Delta.OfString
			events = append(events, ai.EventTextDelta{Text: event.Delta.OfString})
		}

	case "response.output_text.done":
		return s.textSnapshot(textPart{event.ItemID, event.ContentIndex}, event.Text)

	case "response.refusal.done":
		return s.textSnapshot(textPart{event.ItemID, event.ContentIndex}, event.Refusal)

	case "response.output_item.added", "response.output_item.done":
		return s.itemSnapshot(event.Item)

	case "response.function_call_arguments.delta":
		callID, ok := s.itemToCall[event.ItemID]
		if !ok {
			return nil, errors.New("responses tool arguments arrived without a call identity")
		}
		call := s.calls[callID]
		call.arguments += event.Delta.OfString
		s.calls[callID] = call
		events = append(events, ai.EventToolCallDelta{
			ID:        callID,
			Arguments: event.Delta.OfString,
		})

	case "response.function_call_arguments.done":
		callID, ok := s.itemToCall[event.ItemID]
		if !ok {
			return nil, errors.New("responses final tool arguments arrived without a call identity")
		}
		return s.argumentsSnapshot(callID, event.Arguments)

	case "response.completed":
		for _, item := range event.Response.Output {
			recovered, err := s.itemSnapshot(item)
			if err != nil {
				return nil, err
			}
			events = append(events, recovered...)
		}
		usage := event.Response.Usage
		if usage.TotalTokens > 0 || usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.InputTokensDetails.CachedTokens > 0 {
			// input_tokens includes cached_tokens; ai.Usage keeps the two
			// disjoint so each is priced at its own rate.
			modelUsage := ai.UsageWithCachedInput(
				int(usage.InputTokens),
				int(usage.OutputTokens),
				int(usage.InputTokensDetails.CachedTokens),
				int(usage.TotalTokens),
			)
			modelUsage.ReasoningTokens = int(usage.OutputTokensDetails.ReasoningTokens)
			events = append(events, ai.EventUsage{Usage: modelUsage})
		}
		events = append(events, ai.EventStop{Reason: mapStopReason(event.Response.Status)})

	case "response.failed":
		events = append(events, ai.EventStop{Reason: ai.StopReasonError})

	case "response.incomplete":
		events = append(events, ai.EventStop{Reason: ai.StopReasonLength})
	}

	return events, nil
}

func (s *streamState) itemSnapshot(item responses.ResponseOutputItemUnion) ([]ai.AssistantEvent, error) {
	if item.Type == "function_call" {
		return s.callSnapshot(item)
	}
	var events []ai.AssistantEvent
	if item.Type == "message" {
		for i, part := range item.Content {
			text := part.Text
			if part.Type == "refusal" {
				text = part.Refusal
			}
			recovered, err := s.textSnapshot(textPart{item.ID, int64(i)}, text)
			if err != nil {
				return nil, err
			}
			events = append(events, recovered...)
		}
	}
	return events, nil
}

func (s *streamState) textSnapshot(part textPart, full string) ([]ai.AssistantEvent, error) {
	suffix, ok := strings.CutPrefix(full, s.text[part])
	if !ok {
		return nil, errors.New("responses text snapshot conflicts with streamed text")
	}
	s.text[part] = full
	if suffix == "" {
		return nil, nil
	}
	return []ai.AssistantEvent{ai.EventTextDelta{Text: suffix}}, nil
}

func (s *streamState) callSnapshot(item responses.ResponseOutputItemUnion) ([]ai.AssistantEvent, error) {
	if item.CallID == "" || item.Name == "" {
		return nil, errors.New("responses tool snapshot is missing its call identity")
	}
	if previous := s.itemToCall[item.ID]; previous != "" && previous != item.CallID {
		return nil, errors.New("responses tool item changed call identity")
	}
	s.itemToCall[item.ID] = item.CallID
	call, seen := s.calls[item.CallID]
	if seen && call.name != item.Name {
		return nil, errors.New("responses tool snapshot changed function name")
	}
	call.name = item.Name
	s.calls[item.CallID] = call
	var events []ai.AssistantEvent
	if !seen {
		events = append(events, ai.EventToolCallDelta{ID: item.CallID, Name: item.Name})
	}
	remaining, err := s.argumentsSnapshot(item.CallID, item.Arguments)
	if err != nil {
		return nil, err
	}
	return append(events, remaining...), nil
}

func (s *streamState) argumentsSnapshot(callID, full string) ([]ai.AssistantEvent, error) {
	call := s.calls[callID]
	// Both deltas and final snapshots describe the same bytes. Emit only the
	// missing suffix so a done item repeated in response.completed executes once.
	suffix, ok := strings.CutPrefix(full, call.arguments)
	if !ok {
		// Never include arguments here: they can contain tool credentials.
		return nil, fmt.Errorf("responses tool %q snapshot conflicts with streamed arguments", callID)
	}
	if suffix == "" {
		return nil, nil
	}
	call.arguments = full
	s.calls[callID] = call
	return []ai.AssistantEvent{ai.EventToolCallDelta{ID: callID, Arguments: suffix}}, nil
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
