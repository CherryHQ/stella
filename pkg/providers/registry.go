package providers

import (
	"context"

	"github.com/CherryHQ/stella/pkg/ai"
)

// Complete consumes a full stream from a StreamFunc and assembles an assistant message.
func Complete(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.CompleteOptions, stream StreamFunc) (ai.AssistantMessage, error) {
	eventStream, err := stream(goCtx, model, ctx, opts.StreamOptions)
	if err != nil {
		return ai.AssistantMessage{}, err
	}

	msg := ai.AssistantMessage{Content: make([]ai.ContentBlock, 0, 4)}
	var text string
	var thinking string
	toolCalls := map[string]ai.ToolCall{}
	// The map merges deltas by ID; this slice preserves provider emission order.
	toolCallOrder := make([]string, 0, 4)
	toolArgs := map[string]string{}

	for event := range eventStream.Events() {
		switch e := event.(type) {
		case ai.EventTextDelta:
			text += e.Text
		case ai.EventThinkingDelta:
			thinking += e.Thinking
		case ai.EventToolCallDelta:
			if _, ok := toolCalls[e.ID]; !ok {
				toolCallOrder = append(toolCallOrder, e.ID)
			}
			call := toolCalls[e.ID]
			call.ID = e.ID
			if e.Name != "" {
				call.Name = e.Name
			}
			if e.Arguments != "" {
				toolArgs[e.ID] += e.Arguments
			}
			toolCalls[e.ID] = call
		case ai.EventUsage:
			msg.Usage = e.Usage
		case ai.EventStop:
			msg.StopReason = e.Reason
		case ai.EventError:
			if e.Err != nil {
				msg.ErrorMessage = e.Err.Error()
				msg.StopReason = ai.StopReasonError
			}
		}
	}

	if waitErr := eventStream.Wait(); waitErr != nil {
		return msg, waitErr
	}

	if text != "" {
		msg.Content = append(msg.Content, ai.TextContent{Text: text})
	}
	if thinking != "" {
		msg.Content = append(msg.Content, ai.ThinkingContent{Thinking: thinking})
	}
	for _, id := range toolCallOrder {
		call := toolCalls[id]
		if raw := toolArgs[id]; raw != "" {
			call.Arguments = map[string]any{"raw": raw}
		}
		msg.Content = append(msg.Content, call)
	}

	return msg, nil
}
