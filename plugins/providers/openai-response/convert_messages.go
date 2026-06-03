package openairesponse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"

	"github.com/CherryHQ/stella/pkg/ai"
)

func convertMessages(ctx ai.Context) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(ctx.Messages))

	for _, msg := range ctx.Messages {
		switch m := msg.(type) {
		case ai.UserMessage:
			items = append(items, userMessage(m.TimestampedContent()))
		case ai.AssistantMessage:
			items = append(items, convertAssistantMessage(m)...)
		case ai.ToolResultMessage:
			items = append(items, toolResultItems(m)...)
		}
	}
	return items
}

// toolResultItems converts a tool result into response input items. A function
// call output only carries string content, so an image result is emitted as a
// text output followed by a user message holding the image inline — the only
// place the API lets a tool's image reach the model.
func toolResultItems(m ai.ToolResultMessage) responses.ResponseInputParam {
	text := ai.FlattenText(m.Content)
	output := responses.ResponseInputItemUnionParam{
		OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: m.ToolCallID,
			Output: text,
		},
	}
	if !ai.HasImage(m.Content) {
		return responses.ResponseInputParam{output}
	}

	if text == "" {
		output.OfFunctionCallOutput.Output = "[image returned by tool; see the following message]"
	}
	parts := make(responses.ResponseInputMessageContentListParam, 0, len(m.Content))
	for _, block := range m.Content {
		if img, ok := block.(ai.ImageContent); ok {
			parts = append(parts, responses.ResponseInputContentUnionParam{
				OfInputImage: &responses.ResponseInputImageParam{
					ImageURL: param.NewOpt(img.DataURI()),
				},
			})
		}
	}
	return responses.ResponseInputParam{
		output,
		{OfMessage: &responses.EasyInputMessageParam{
			Role: responses.EasyInputMessageRoleUser,
			Content: responses.EasyInputMessageContentUnionParam{
				OfInputItemContentList: parts,
			},
		}},
	}
}

func convertAssistantMessage(m ai.AssistantMessage) responses.ResponseInputParam {
	var items responses.ResponseInputParam
	var textParts []string

	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.TextContent:
			textParts = append(textParts, b.Text)
		case ai.ToolCall:
			argsJSON, _ := json.Marshal(b.Arguments)
			items = append(items, responses.ResponseInputItemUnionParam{
				OfFunctionCall: &responses.ResponseFunctionToolCallParam{
					Arguments: string(argsJSON),
					CallID:    b.ID,
					Name:      b.Name,
				},
			})
		}
	}

	if text := strings.Join(textParts, " "); text != "" {
		items = append([]responses.ResponseInputItemUnionParam{{
			OfMessage: &responses.EasyInputMessageParam{
				Role: responses.EasyInputMessageRoleAssistant,
				Content: responses.EasyInputMessageContentUnionParam{
					OfString: param.NewOpt(text),
				},
			},
		}}, items...)
	}

	return items
}

func userMessage(content any) responses.ResponseInputItemUnionParam {
	textMsg := func(s string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{
			OfMessage: &responses.EasyInputMessageParam{
				Role: responses.EasyInputMessageRoleUser,
				Content: responses.EasyInputMessageContentUnionParam{
					OfString: param.NewOpt(s),
				},
			},
		}
	}

	switch c := content.(type) {
	case string:
		return textMsg(c)
	case []ai.ContentBlock:
		if !ai.HasImage(c) {
			return textMsg(ai.FlattenText(c))
		}
		parts := make(responses.ResponseInputMessageContentListParam, 0, len(c))
		for _, block := range c {
			switch b := block.(type) {
			case ai.TextContent:
				parts = append(parts, responses.ResponseInputContentParamOfInputText(b.Text))
			case ai.ImageContent:
				img := responses.ResponseInputContentUnionParam{
					OfInputImage: &responses.ResponseInputImageParam{
						ImageURL: param.NewOpt(b.DataURI()),
					},
				}
				parts = append(parts, img)
			}
		}
		return responses.ResponseInputItemUnionParam{
			OfMessage: &responses.EasyInputMessageParam{
				Role: responses.EasyInputMessageRoleUser,
				Content: responses.EasyInputMessageContentUnionParam{
					OfInputItemContentList: parts,
				},
			},
		}
	default:
		return textMsg(fmt.Sprintf("%v", content))
	}
}
