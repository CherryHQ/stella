package openairesponse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/vaayne/anna/ai/types"
)

func convertMessages(ctx types.Context) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(ctx.Messages))

	for _, msg := range ctx.Messages {
		switch m := msg.(type) {
		case types.UserMessage:
			items = append(items, userMessage(m.Content))
		case types.AssistantMessage:
			items = append(items, convertAssistantMessage(m)...)
		case types.ToolResultMessage:
			items = append(items, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: m.ToolCallID,
					Output: types.FlattenText(m.Content),
				},
			})
		}
	}
	return items
}

func convertAssistantMessage(m types.AssistantMessage) responses.ResponseInputParam {
	var items responses.ResponseInputParam
	var textParts []string

	for _, block := range m.Content {
		switch b := block.(type) {
		case types.TextContent:
			textParts = append(textParts, b.Text)
		case types.ToolCall:
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
	case []types.ContentBlock:
		if !types.HasImage(c) {
			return textMsg(types.FlattenText(c))
		}
		parts := make(responses.ResponseInputMessageContentListParam, 0, len(c))
		for _, block := range c {
			switch b := block.(type) {
			case types.TextContent:
				parts = append(parts, responses.ResponseInputContentParamOfInputText(b.Text))
			case types.ImageContent:
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
