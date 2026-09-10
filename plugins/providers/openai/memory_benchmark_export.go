//go:build personamemeval

package openai

import (
	"context"

	"github.com/openai/openai-go/option"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// NewDeepSeekThinkingDisabledBenchmarkStream uses the production OpenAI
// adapter while explicitly sending both gateway and DeepSeek non-thinking
// controls. The helper is absent from normal builds.
func NewDeepSeekThinkingDisabledBenchmarkStream(cfg Config) providers.StreamFunc {
	provider := New(cfg)
	return func(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
		requestCtx := goCtx
		cancel := func() {}
		if opts.Timeout > 0 {
			requestCtx, cancel = context.WithTimeout(goCtx, opts.Timeout)
		}
		params := buildParams(model, ctx, opts)
		reqOpts := buildRequestOptions(opts)
		reqOpts = append(reqOpts, option.WithJSONSet("reasoning_effort", "none"))
		reqOpts = append(reqOpts, option.WithJSONSet("thinking", map[string]string{"type": "disabled"}))
		sdkStream := provider.client.Chat.Completions.NewStreaming(requestCtx, params, reqOpts...)

		out := providers.NewChannelEventStream(32)
		go func() {
			defer cancel()
			defer func() { _ = sdkStream.Close() }()
			defer out.Finish(nil)
			out.Emit(ai.EventStart{})
			completed := consumeStream(sdkStream, out)
			if err := sdkStream.Err(); err != nil && !completed {
				out.Emit(ai.EventError{Err: err})
			}
		}()
		return out, nil
	}
}
