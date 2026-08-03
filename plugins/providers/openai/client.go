package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/httpclient"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

func init() {
	pkgplugins.Register("provider/openai", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           "provider/openai",
			Kind:         "provider",
			Name:         "openai",
			DisplayName:  "OpenAI",
			Description:  "OpenAI Chat Completions API provider.",
			AdminVisible: true,
			Capabilities: []string{
				pkgplugins.CapabilityProvider,
			},
		})
		host.AddProvider(pkgplugins.ProviderSpec{
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

// stripLeadingSSEComments removes leading SSE comment lines (lines starting
// with ':') and any blank lines that follow them. Some proxies send a
// ": keep-alive" comment as the first event; the openai-go SSE parser fails
// on these empty-data events with "unexpected end of JSON input".
func stripLeadingSSEComments(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp, nil
	}
	resp.Body = &sseCommentStripper{rc: resp.Body}
	return resp, nil
}

// sseCommentStripper wraps a ReadCloser and skips leading SSE comment lines
// (any line beginning with ':') and the blank lines that separate SSE events.
type sseCommentStripper struct {
	rc   io.ReadCloser
	buf  []byte
	done bool
}

func (s *sseCommentStripper) Read(p []byte) (int, error) {
	if s.done {
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			return n, nil
		}
		return s.rc.Read(p)
	}

	for !s.done {
		// Drain comment lines and blank separator lines from the front of buf.
		for len(s.buf) > 0 {
			b := s.buf[0]
			switch b {
			case ':':
				// Skip entire comment line.
				idx := bytes.IndexByte(s.buf, '\n')
				if idx < 0 {
					break // Need more data to find end of comment line.
				}
				s.buf = s.buf[idx+1:]
			case '\n', '\r':
				s.buf = s.buf[1:]
			default:
				s.done = true
			}
			if s.done {
				// Stop draining as soon as the first normal SSE payload byte is found.
				break
			}
		}
		if s.done {
			break
		}
		// Need more data.
		tmp := make([]byte, 512)
		n, err := s.rc.Read(tmp)
		s.buf = append(s.buf, tmp[:n]...)
		if err != nil {
			s.done = true
			break
		}
	}

	if len(s.buf) == 0 {
		return s.rc.Read(p)
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *sseCommentStripper) Close() error { return s.rc.Close() }

// New returns an OpenAI provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{
		option.WithHTTPClient(httpclient.StdHTTPClient()),
		option.WithMiddleware(stripLeadingSSEComments),
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
	for k, v := range opts.Headers {
		reqOpts = append(reqOpts, option.WithHeader(k, v))
	}
	return reqOpts
}
