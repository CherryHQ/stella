package ai

import "time"

// TransformMessages applies compatibility normalization needed by provider adapters.
func TransformMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages)+2)
	now := time.Now().UTC()

	for i := range messages {
		msg := messages[i]
		out = append(out, msg)

		assistant, ok := msg.(AssistantMessage)
		if !ok {
			continue
		}

		for _, block := range assistant.Content {
			call, ok := block.(ToolCall)
			if !ok {
				continue
			}
			if hasToolResultFor(messages, i+1, call.ID) {
				continue
			}
			out = append(out, ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content: []ContentBlock{
					TextContent{Text: "synthetic tool result: missing tool output"},
				},
				IsError:   true,
				Timestamp: now,
			})
		}
	}

	return out
}

func hasToolResultFor(messages []Message, start int, toolCallID string) bool {
	for i := start; i < len(messages); i++ {
		toolResult, ok := messages[i].(ToolResultMessage)
		if !ok {
			continue
		}
		if toolResult.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}
