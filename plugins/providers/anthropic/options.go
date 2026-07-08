package anthropic

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/CherryHQ/stella/pkg/ai"
)

const defaultMaxTokens = 16384

func buildParams(model ai.Model, ctx ai.Context, opts ai.StreamOptions) sdk.MessageNewParams {
	maxTokens := int64(defaultMaxTokens)
	if model.MaxTokens > 0 {
		maxTokens = int64(model.MaxTokens)
	}
	if opts.MaxTokens != nil {
		maxTokens = int64(*opts.MaxTokens)
	}

	params := sdk.MessageNewParams{
		Model:     sdk.Model(model.Name),
		MaxTokens: maxTokens,
		Messages:  convertMessages(ctx),
	}

	if ctx.System != "" {
		params.System = []sdk.TextBlockParam{{Text: ctx.System}}
	}

	if opts.Temperature != nil {
		params.Temperature = sdk.Float(*opts.Temperature)
	}

	if len(ctx.Tools) > 0 {
		params.Tools = convertTools(ctx.Tools)
	}

	return params
}

func convertTools(tools []ai.ToolDefinition) []sdk.ToolUnionParam {
	out := make([]sdk.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tp := sdk.ToolParam{
			Name:        t.Name,
			InputSchema: toolSchema(t.InputSchema),
			Description: param.NewOpt(t.Description),
		}
		out = append(out, sdk.ToolUnionParam{OfTool: &tp})
	}
	return out
}

// toolSchema carries the full JSON Schema to Anthropic. The SDK's typed struct
// only models `properties`, `required`, and `type`, so every other top-level key
// (oneOf/anyOf/$defs, used by multi-action tool schemas to hold per-action
// parameters) must ride along in ExtraFields or it is silently dropped.
func toolSchema(in map[string]any) sdk.ToolInputSchemaParam {
	schema := sdk.ToolInputSchemaParam{Properties: in["properties"]}
	schema.Required = stringSlice(in["required"])
	var extras map[string]any
	for k, v := range in {
		switch k {
		case "type", "properties", "required":
			continue
		}
		if extras == nil {
			extras = map[string]any{}
		}
		extras[k] = v
	}
	schema.ExtraFields = extras
	return schema
}

// stringSlice coerces a JSON-decoded array (which is []any after unmarshalling
// into map[string]any) into []string, tolerating an already-typed []string.
func stringSlice(v any) []string {
	switch req := v.(type) {
	case []string:
		return req
	case []any:
		out := make([]string, 0, len(req))
		for _, item := range req {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
