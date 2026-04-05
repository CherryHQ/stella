package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/vaayne/anna/internal/ai"
)

func convertMessages(ctx ai.Context) []sdk.ChatCompletionMessageParamUnion {
	messages := make([]sdk.ChatCompletionMessageParamUnion, 0, len(ctx.Messages)+1)

	if ctx.System != "" {
		messages = append(messages, sdk.SystemMessage(ctx.System))
	}

	for _, msg := range ctx.Messages {
		switch m := msg.(type) {
		case ai.UserMessage:
			messages = append(messages, userMessage(m.Content))
		case ai.AssistantMessage:
			messages = append(messages, convertAssistantMessage(m))
		case ai.ToolResultMessage:
			messages = append(messages, sdk.ToolMessage(ai.FlattenText(m.Content), m.ToolCallID))
		}
	}
	return messages
}

func convertAssistantMessage(m ai.AssistantMessage) sdk.ChatCompletionMessageParamUnion {
	var toolCalls []sdk.ChatCompletionMessageToolCallParam
	var textParts []string

	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.TextContent:
			textParts = append(textParts, b.Text)
		case ai.ToolCall:
			argsJSON, _ := json.Marshal(b.Arguments)
			toolCalls = append(toolCalls, sdk.ChatCompletionMessageToolCallParam{
				ID: b.ID,
				Function: sdk.ChatCompletionMessageToolCallFunctionParam{
					Name:      b.Name,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	if len(toolCalls) > 0 {
		assistant := sdk.ChatCompletionAssistantMessageParam{
			ToolCalls: toolCalls,
		}
		if text := strings.Join(textParts, " "); text != "" {
			assistant.Content.OfString = param.NewOpt(text)
		}
		return sdk.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
	}

	return sdk.AssistantMessage(strings.Join(textParts, " "))
}

func userMessage(content any) sdk.ChatCompletionMessageParamUnion {
	switch c := content.(type) {
	case string:
		return sdk.UserMessage(c)
	case []ai.ContentBlock:
		if !ai.HasImage(c) {
			return sdk.UserMessage(ai.FlattenText(c))
		}
		parts := make([]sdk.ChatCompletionContentPartUnionParam, 0, len(c))
		for _, block := range c {
			switch b := block.(type) {
			case ai.TextContent:
				parts = append(parts, sdk.TextContentPart(b.Text))
			case ai.ImageContent:
				parts = append(parts, sdk.ImageContentPart(sdk.ChatCompletionContentPartImageImageURLParam{
					URL: b.DataURI(),
				}))
			}
		}
		return sdk.UserMessage(parts)
	default:
		return sdk.UserMessage(fmt.Sprintf("%v", content))
	}
}
