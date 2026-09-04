package anthropic

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/httpclient"
	"github.com/CherryHQ/stella/pkg/providers"
)

// Definition returns Anthropic's compiled-in provider definition.
func Definition() providers.Definition {
	return providers.Definition{
		ID:         "anthropic",
		Name:       "Anthropic",
		DefaultURL: "https://api.anthropic.com",
		Build: func(config providers.Config) (providers.ProviderAdapter, error) {
			return New(Config{APIKey: config.APIKey, BaseURL: config.BaseURL}), nil
		},
	}
}

// Config configures the Anthropic provider.
type Config struct {
	BaseURL string
	APIKey  string
}

// Provider implements providers.ProviderAdapter for Anthropic messages.
type Provider struct {
	client anthropic.Client
}

// New returns an Anthropic provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{
		option.WithHTTPClient(httpclient.StdHTTPClient()),
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Provider{client: anthropic.NewClient(opts...)}
}

// API returns provider key.
func (p *Provider) API() string { return "anthropic" }

// Stream starts Anthropic message stream.
func (p *Provider) Stream(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
	params := buildParams(model, ctx, opts)
	reqOpts := buildRequestOptions(opts)
	sdkStream := p.client.Messages.NewStreaming(goCtx, params, reqOpts...)

	out := providers.NewChannelEventStream(32)
	go func() {
		defer func() { _ = sdkStream.Close() }()
		defer out.Finish(nil)
		completed := consumeStream(sdkStream, out)
		if err := sdkStream.Err(); err != nil && !completed {
			out.Emit(ai.EventError{Err: err})
		}
	}()
	return out, nil
}

// ListModels fetches available models from the Anthropic API.
func (p *Provider) ListModels(ctx context.Context) ([]ai.Model, error) {
	page, err := p.client.Models.List(ctx, anthropic.ModelListParams{})
	if err != nil {
		return nil, fmt.Errorf("anthropic list models: %w", err)
	}

	var models []ai.Model
	for _, m := range page.Data {
		models = append(models, ai.Model{
			ID:       m.ID,
			Name:     m.ID,
			API:      "anthropic",
			Provider: "anthropic",
		})
	}
	return models, nil
}

var _ providers.ModelLister = (*Provider)(nil)

func buildRequestOptions(opts ai.StreamOptions) []option.RequestOption {
	var reqOpts []option.RequestOption
	for k, v := range opts.Headers {
		reqOpts = append(reqOpts, option.WithHeader(k, v))
	}
	return reqOpts
}
