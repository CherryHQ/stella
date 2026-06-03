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

	for i := 0; i < len(ctx.Messages); i++ {
		switch m := ctx.Messages[i].(type) {
		case ai.UserMessage:
			items = append(items, userMessage(m.TimestampedContent()))
		case ai.AssistantMessage:
			items = append(items, convertAssistantMessage(m)...)
		case ai.ToolResultMessage:
			i = appendToolResults(&items, ctx.Messages, i)
		}
	}
	return items
}

// appendToolResults converts the run of consecutive tool results starting at
// index start, returning the index of the last one consumed. A function call
// output only carries string content, so images are siphoned out and appended
// as a single user message *after* every function output in the run — inserting
// the image carrier between outputs would break the function-call output chain.
//
// Each result's images are preceded by a text label naming the originating tool
// and call ID, so a multi-result turn keeps every image attributable to its
// source instead of relying on positional guessing.
func appendToolResults(items *responses.ResponseInputParam, msgs []ai.Message, start int) int {
	var parts responses.ResponseInputMessageContentListParam
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
			parts = append(parts, responses.ResponseInputContentParamOfInputText(toolImageLabel(m)))
			for _, block := range m.Content {
				if img, ok := block.(ai.ImageContent); ok {
					parts = append(parts, responses.ResponseInputContentUnionParam{
						OfInputImage: &responses.ResponseInputImageParam{
							ImageURL: param.NewOpt(img.DataURI()),
						},
					})
				}
			}
		}
		*items = append(*items, responses.ResponseInputItemUnionParam{
			OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
				CallID: m.ToolCallID,
				Output: text,
			},
		})
		i++
	}
	if len(parts) > 0 {
		*items = append(*items, responses.ResponseInputItemUnionParam{
			OfMessage: &responses.EasyInputMessageParam{
				Role: responses.EasyInputMessageRoleUser,
				Content: responses.EasyInputMessageContentUnionParam{
					OfInputItemContentList: parts,
				},
			},
		})
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
