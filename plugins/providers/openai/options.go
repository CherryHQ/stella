package openai

import (
	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"

	"github.com/CherryHQ/stella/pkg/ai"
)

func buildParams(model ai.Model, ctx ai.Context, opts ai.StreamOptions) sdk.ChatCompletionNewParams {
	messages := convertMessages(ctx)

	params := sdk.ChatCompletionNewParams{
		Model:    model.Name,
		Messages: messages,
		StreamOptions: sdk.ChatCompletionStreamOptionsParam{
			IncludeUsage: sdk.Bool(true),
		},
	}

	if opts.Temperature != nil {
		params.Temperature = sdk.Float(*opts.Temperature)
	}
	if opts.MaxTokens != nil {
		params.MaxCompletionTokens = sdk.Int(int64(*opts.MaxTokens))
	} else if model.MaxTokens > 0 {
		params.MaxCompletionTokens = sdk.Int(int64(model.MaxTokens))
	}

	if len(ctx.Tools) > 0 {
		params.Tools = convertTools(ctx.Tools)
	}

	return params
}

func convertTools(tools []ai.ToolDefinition) []sdk.ChatCompletionToolParam {
	out := make([]sdk.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, sdk.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  shared.FunctionParameters(t.InputSchema),
			},
		})
	}
	return out
}
