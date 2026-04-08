package openai

import (
	"context"
	"fmt"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/httpclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
)

func init() {
	pkgplugins.Register("provider/openai", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:           "provider/openai",
			Kind:         "provider",
			Name:         "openai",
			DisplayName:  "OpenAI",
			AdminVisible: true,
			Capabilities: []string{
				pkgplugins.CapabilityProvider,
			},
		})
		host.Registry().RegisterProvider(pkgplugins.ProviderRegistration{
			PluginID: "provider/openai",
			Name:     "openai",
			Meta: pkgplugins.ProviderMeta{
				Name:       "OpenAI",
				DefaultURL: "https://api.openai.com/v1",
			},
			Build: func(ctx pkgplugins.ProviderContext) (providers.ProviderAdapter, error) {
				apiKey, _ := ctx.State.Config["api_key"].(string)
				baseURL, _ := ctx.State.Config["base_url"].(string)
				return New(Config{APIKey: apiKey, BaseURL: baseURL}), nil
			},
		})
	}))
}

// Config configures the OpenAI provider.
type Config struct {
	BaseURL string
	APIKey  string
}

// Provider implements providers.ProviderAdapter for OpenAI chat completions.
type Provider struct {
	client sdk.Client
}

// New returns an OpenAI provider.
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
	return &Provider{client: sdk.NewClient(opts...)}
}

// API returns provider key.
func (p *Provider) API() string { return "openai" }

// Stream starts OpenAI chat completion stream.
func (p *Provider) Stream(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
	params := buildParams(model, ctx, opts)
	reqOpts := buildRequestOptions(opts)
	sdkStream := p.client.Chat.Completions.NewStreaming(goCtx, params, reqOpts...)

	out := providers.NewChannelEventStream(32)
	go func() {
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

// StreamSimple delegates to Stream with mapped options.
func (p *Provider) StreamSimple(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return p.Stream(goCtx, model, ctx, opts.StreamOptions)
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
