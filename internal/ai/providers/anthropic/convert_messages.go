package anthropic

import (
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/vaayne/anna/internal/ai"
)

func convertMessages(ctx ai.Context) []sdk.MessageParam {
	messages := make([]sdk.MessageParam, 0, len(ctx.Messages))
	for _, msg := range ctx.Messages {
		switch m := msg.(type) {
		case ai.UserMessage:
			messages = append(messages, sdk.NewUserMessage(userContentBlocks(m.Content)...))
		case ai.AssistantMessage:
			messages = append(messages, sdk.NewAssistantMessage(assistantContentBlocks(m.Content)...))
		case ai.ToolResultMessage:
			messages = append(messages, sdk.NewUserMessage(toolResultBlock(m)))
		}
	}
	return messages
}

func userContentBlocks(content any) []sdk.ContentBlockParamUnion {
	switch c := content.(type) {
	case string:
		return []sdk.ContentBlockParamUnion{sdk.NewTextBlock(c)}
	case []ai.ContentBlock:
		blocks := make([]sdk.ContentBlockParamUnion, 0, len(c))
		for _, block := range c {
			switch b := block.(type) {
			case ai.TextContent:
				blocks = append(blocks, sdk.NewTextBlock(b.Text))
			case ai.ImageContent:
				blocks = append(blocks, sdk.NewImageBlockBase64(b.MimeType, b.Data))
			}
		}
		return blocks
	default:
		return []sdk.ContentBlockParamUnion{sdk.NewTextBlock(fmt.Sprintf("%v", content))}
	}
}

func assistantContentBlocks(blocks []ai.ContentBlock) []sdk.ContentBlockParamUnion {
	out := make([]sdk.ContentBlockParamUnion, 0, len(blocks))
	for _, block := range blocks {
		switch b := block.(type) {
		case ai.TextContent:
			out = append(out, sdk.NewTextBlock(b.Text))
		case ai.ThinkingContent:
			out = append(out, sdk.NewThinkingBlock(b.Signature, b.Thinking))
		case ai.ToolCall:
			out = append(out, sdk.NewToolUseBlock(b.ID, b.Arguments, b.Name))
		}
	}
	return out
}

func toolResultBlock(m ai.ToolResultMessage) sdk.ContentBlockParamUnion {
	text := ""
	for _, block := range m.Content {
		if t, ok := block.(ai.TextContent); ok && t.Text != "" {
			text = t.Text
			break
		}
	}
	return sdk.NewToolResultBlock(m.ToolCallID, text, m.IsError)
}
