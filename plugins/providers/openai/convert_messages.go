package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"

	"github.com/CherryHQ/stella/pkg/ai"
)

func convertMessages(ctx ai.Context) []sdk.ChatCompletionMessageParamUnion {
	messages := make([]sdk.ChatCompletionMessageParamUnion, 0, len(ctx.Messages)+1)

	if ctx.System != "" {
		messages = append(messages, sdk.SystemMessage(ctx.System))
	}

	for i := 0; i < len(ctx.Messages); i++ {
		switch m := ctx.Messages[i].(type) {
		case ai.UserMessage:
			messages = append(messages, userMessage(m.TimestampedContent()))
		case ai.AssistantMessage:
			messages = append(messages, convertAssistantMessage(m))
		case ai.ToolResultMessage:
			i = appendToolResults(&messages, ctx.Messages, i)
		}
	}
	return messages
}

// appendToolResults converts the run of consecutive tool results starting at
// index start, returning the index of the last one consumed. The Chat
// Completions tool role only accepts string content, so images are siphoned out
// and appended as a single user message *after* every tool message in the run —
// inserting the image carrier between tool messages would break the API's
// requirement that each assistant tool_call be answered before any other role.
//
// Each result's images are preceded by a text label naming the originating tool
// and call ID, so a multi-result turn keeps every image attributable to its
// source instead of relying on positional guessing.
func appendToolResults(messages *[]sdk.ChatCompletionMessageParamUnion, msgs []ai.Message, start int) int {
	var carrier []sdk.ChatCompletionContentPartUnionParam
	i := start
	for i < len(msgs) {
		m, ok := msgs[i].(ai.ToolResultMessage)
		if !ok {
			break
		}
		text := ai.FlattenText(m.Content)
		if ai.HasImage(m.Content) {
			if text == "" {
				text = "[image returned by tool; see the following message]"
			}
			carrier = append(carrier, sdk.TextContentPart(toolImageLabel(m)))
			for _, block := range m.Content {
				if img, ok := block.(ai.ImageContent); ok {
					carrier = append(carrier, sdk.ImageContentPart(sdk.ChatCompletionContentPartImageImageURLParam{
						URL: img.DataURI(),
					}))
				}
			}
		}
		*messages = append(*messages, sdk.ToolMessage(text, m.ToolCallID))
		i++
	}
	if len(carrier) > 0 {
		*messages = append(*messages, sdk.UserMessage(carrier))
	}
	return i - 1
}

// toolImageLabel describes which tool result the following images belong to, so
// the model can attribute them when several tools return images in one turn.
func toolImageLabel(m ai.ToolResultMessage) string {
	name := m.ToolName
	if name == "" {
		name = "tool"
	}
	return fmt.Sprintf("Images from %s result (tool_call_id %s):", name, m.ToolCallID)
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
