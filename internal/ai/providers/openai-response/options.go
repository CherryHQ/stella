package openairesponse

import (
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/toolspec"
)

func buildParams(model ai.Model, ctx ai.Context, opts ai.StreamOptions) responses.ResponseNewParams {
	input := convertMessages(ctx)

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(model.Name),
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
	}

	if len(ctx.Tools) > 0 {
		params.Tools = convertTools(ctx.Tools)
	}

	return params
}

func convertTools(tools []toolspec.Definition) []responses.ToolUnionParam {
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
