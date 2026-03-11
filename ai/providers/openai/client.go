package openai

import (
	"context"
	"fmt"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/vaayne/anna/ai"
)

// Config configures the OpenAI provider.
type Config struct {
	BaseURL string
	APIKey  string
}

// Provider implements ai.ProviderAdapter for OpenAI chat completions.
type Provider struct {
	client sdk.Client
}

// New returns an OpenAI provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Provider{client: sdk.NewClient(opts...)}
}

// API returns provider key.
func (p *Provider) API() string { return "openai" }

// Stream starts OpenAI chat completion stream.
func (p *Provider) Stream(model ai.Model, ctx ai.Context, opts ai.StreamOptions) (ai.AssistantEventStream, error) {
	params := buildParams(model, ctx, opts)
	reqOpts := buildRequestOptions(opts)
	sdkStream := p.client.Chat.Completions.NewStreaming(context.Background(), params, reqOpts...)

	out := ai.NewChannelEventStream(32)
	go func() {
		defer out.Finish(nil)
		out.Emit(ai.EventStart{})
		consumeStream(sdkStream, out)
		if err := sdkStream.Err(); err != nil {
			out.Emit(ai.EventError{Err: err})
		}
	}()
	return out, nil
}

// StreamSimple delegates to Stream with mapped options.
func (p *Provider) StreamSimple(model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions) (ai.AssistantEventStream, error) {
	return p.Stream(model, ctx, opts.StreamOptions)
}

// ListModels fetches available models from the OpenAI API.
func (p *Provider) ListModels(ctx context.Context) ([]ai.Model, error) {
	page, err := p.client.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("openai list models: %w", err)
	}

	var models []ai.Model
	for _, m := range page.Data {
		models = append(models, ai.Model{
			ID:       m.ID,
			Name:     m.ID,
			API:      "openai",
			Provider: "openai",
		})
	}
	return models, nil
}

var _ ai.ModelLister = (*Provider)(nil)

func buildRequestOptions(opts ai.StreamOptions) []option.RequestOption {
	var reqOpts []option.RequestOption
	if opts.APIKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(opts.BaseURL))
	}
	for k, v := range opts.Headers {
		reqOpts = append(reqOpts, option.WithHeader(k, v))
	}
	return reqOpts
}
