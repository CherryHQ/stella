package openairesponse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/httpclient"
	"github.com/CherryHQ/stella/pkg/providers"
)

// Definition returns OpenAI Responses' compiled-in provider definition.
func Definition() providers.Definition {
	return providers.Definition{
		ID:         "openai-response",
		Name:       "OpenAI Response",
		DefaultURL: "https://api.openai.com/v1",
		Build: func(config providers.Config) (providers.ProviderAdapter, error) {
			return New(Config{APIKey: config.APIKey, BaseURL: config.BaseURL}), nil
		},
	}
}

// Config configures the OpenAI Responses provider.
type Config struct {
	BaseURL string
	APIKey  string
}

// Provider implements providers.ProviderAdapter for OpenAI Responses API.
type Provider struct {
	client sdk.Client
}

// New returns an OpenAI Responses provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{
		option.WithHTTPClient(httpclient.StdHTTPClient()),
		option.WithMiddleware(stripLeadingNewlines),
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Provider{client: sdk.NewClient(opts...)}
}

// stripLeadingNewlines removes leading newlines from SSE response bodies
// that some proxies prepend, which would cause the SDK's SSE parser to fail.
func stripLeadingNewlines(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		return resp, nil
	}
	resp.Body = &leadingNewlineStripper{rc: resp.Body}
	return resp, nil
}

type leadingNewlineStripper struct {
	rc      io.ReadCloser
	trimmed bool
}

func (s *leadingNewlineStripper) Read(p []byte) (int, error) {
	if s.trimmed {
		return s.rc.Read(p)
	}
	n, err := s.rc.Read(p)
	if n > 0 {
		data := p[:n]
		trimmed := bytes.TrimLeft(data, "\n\r")
		removed := n - len(trimmed)
		if removed > 0 {
			copy(p, trimmed)
			n = len(trimmed)
		}
		if n > 0 {
			s.trimmed = true
		}
	}
	return n, err
}

func (s *leadingNewlineStripper) Close() error {
	return s.rc.Close()
}

// API returns provider key.
func (p *Provider) API() string { return "openai-response" }

// Stream starts an OpenAI Responses API stream.
func (p *Provider) Stream(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
	params := buildParams(model, ctx, opts)
	reqOpts := buildRequestOptions(opts)
	sdkStream := p.client.Responses.NewStreaming(goCtx, params, reqOpts...)

	out := providers.NewChannelEventStream(32)
	go func() {
		defer func() { _ = sdkStream.Close() }()
		defer out.Finish(nil)
		out.Emit(ai.EventStart{})
		completed := consumeStream(sdkStream, out)
		if err := sdkStream.Err(); err != nil && !completed {
			// Only surface SDK errors when the stream didn't reach a terminal
			// event. Some SDK/proxy combinations emit a benign parse error
			// (e.g. "unexpected end of JSON input") after the stream is done.
			out.Emit(ai.EventError{Err: err})
		}
	}()
	return out, nil
}

// ListModels fetches available models from the OpenAI API.
func (p *Provider) ListModels(ctx context.Context) ([]ai.Model, error) {
	page, err := p.client.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("openai-response list models: %w", err)
	}

	var models []ai.Model
	for _, m := range page.Data {
		models = append(models, ai.Model{
			ID:       m.ID,
			Name:     m.ID,
			API:      "openai-response",
			Provider: "openai-response",
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
