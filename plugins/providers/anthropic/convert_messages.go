package anthropic

import (
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/CherryHQ/stella/pkg/ai"
)

func convertMessages(ctx ai.Context) []sdk.MessageParam {
	messages := make([]sdk.MessageParam, 0, len(ctx.Messages))
	for _, msg := range ctx.Messages {
		switch m := msg.(type) {
		case ai.UserMessage:
			blocks := userContentBlocks(m.TimestampedContent())
			mergeOrAppendUser(&messages, blocks)
		case ai.AssistantMessage:
			blocks := assistantContentBlocks(m.Content)
			mergeOrAppendAssistant(&messages, blocks)
		case ai.ToolResultMessage:
			mergeOrAppendUser(&messages, []sdk.ContentBlockParamUnion{toolResultBlock(m)})
		}
	}
	return messages
}

// mergeOrAppendUser appends blocks to the last message if it is a user message,
// otherwise creates a new user message. The Anthropic API requires alternating
// roles, so consecutive user messages (e.g. multiple tool results followed by
// a user message) must be merged into one.
func mergeOrAppendUser(messages *[]sdk.MessageParam, blocks []sdk.ContentBlockParamUnion) {
	if n := len(*messages); n > 0 && (*messages)[n-1].Role == sdk.MessageParamRoleUser {
		(*messages)[n-1].Content = append((*messages)[n-1].Content, blocks...)
		return
	}
	*messages = append(*messages, sdk.NewUserMessage(blocks...))
}

// mergeOrAppendAssistant appends blocks to the last message if it is an assistant
// message, otherwise creates a new assistant message.
func mergeOrAppendAssistant(messages *[]sdk.MessageParam, blocks []sdk.ContentBlockParamUnion) {
	if n := len(*messages); n > 0 && (*messages)[n-1].Role == sdk.MessageParamRoleAssistant {
		(*messages)[n-1].Content = append((*messages)[n-1].Content, blocks...)
		return
	}
	*messages = append(*messages, sdk.NewAssistantMessage(blocks...))
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
			args := b.Arguments
			if args == nil {
				args = map[string]any{}
			}
			out = append(out, sdk.NewToolUseBlock(b.ID, args, b.Name))
		}
	}
	return out
}

func toolResultBlock(m ai.ToolResultMessage) sdk.ContentBlockParamUnion {
	if !ai.HasImage(m.Content) {
		return sdk.NewToolResultBlock(m.ToolCallID, ai.FlattenText(m.Content), m.IsError)
	}

	content := make([]sdk.ToolResultBlockParamContentUnion, 0, len(m.Content))
	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.TextContent:
			if b.Text != "" {
				content = append(content, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{Text: b.Text}})
			}
		case ai.ImageContent:
			content = append(content, sdk.ToolResultBlockParamContentUnion{OfImage: &sdk.ImageBlockParam{
				Source: sdk.ImageBlockParamSourceUnion{OfBase64: &sdk.Base64ImageSourceParam{
					Data:      b.Data,
					MediaType: sdk.Base64ImageSourceMediaType(b.MimeType),
				}},
			}})
		}
	}
	return sdk.ContentBlockParamUnion{OfToolResult: &sdk.ToolResultBlockParam{
		ToolUseID: m.ToolCallID,
		IsError:   param.NewOpt(m.IsError),
		Content:   content,
	}}
}
