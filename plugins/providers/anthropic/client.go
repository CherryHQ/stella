package anthropic

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
)

func init() {
	pluginproviders.Register("anthropic", pluginproviders.ProviderMeta{
		Name:       "Anthropic",
		DefaultURL: "https://api.anthropic.com",
	}, func(cfg pluginproviders.ProviderConfig) providers.ProviderAdapter {
		return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
	})
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
	opts := []option.RequestOption{}
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
func (p *Provider) Stream(model ai.Model, ctx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
	params := buildParams(model, ctx, opts)
	reqOpts := buildRequestOptions(opts)
	sdkStream := p.client.Messages.NewStreaming(context.Background(), params, reqOpts...)

	out := providers.NewChannelEventStream(32)
	go func() {
		defer out.Finish(nil)
		consumeStream(sdkStream, out)
		if err := sdkStream.Err(); err != nil {
			out.Emit(ai.EventError{Err: err})
		}
	}()
	return out, nil
}

// StreamSimple delegates to Stream with mapped options.
func (p *Provider) StreamSimple(model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return p.Stream(model, ctx, opts.StreamOptions)
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
