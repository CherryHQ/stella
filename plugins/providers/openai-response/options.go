package openairesponse

import (
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/vaayne/anna/pkg/ai"
)

func buildParams(model ai.Model, ctx ai.Context, opts ai.StreamOptions) responses.ResponseNewParams {
	input := convertMessages(ctx)

	params := responses.ResponseNewParams{
		Model: model.Name,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
	}

	if ctx.System != "" {
		params.Instructions = param.NewOpt(ctx.System)
	}

	if opts.Temperature != nil {
		params.Temperature = param.NewOpt(*opts.Temperature)
	}
	if opts.MaxTokens != nil {
		params.MaxOutputTokens = param.NewOpt(int64(*opts.MaxTokens))
	} else if model.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(model.MaxTokens))
	}

	if len(ctx.Tools) > 0 {
		params.Tools = convertTools(ctx.Tools)
	}

	return params
}

func convertTools(tools []ai.ToolDefinition) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}
