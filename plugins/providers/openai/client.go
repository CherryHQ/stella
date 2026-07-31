package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/httpclient"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

const maxLeadingSSECommentLineBytes = 8 << 10

var errLeadingSSECommentLineTooLong = errors.New("leading SSE comment line exceeds limit")

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
	resp.Body = &sseCommentStripper{ctx: req.Context(), rc: resp.Body}
	return resp, nil
}

// sseCommentStripper wraps a ReadCloser and skips leading SSE comment lines
// (any line beginning with ':') and the blank lines that separate SSE events.
type sseCommentStripper struct {
	ctx         context.Context
	rc          io.ReadCloser
	buf         []byte
	done        bool
	terminalErr error
	closeOnce   sync.Once
	closeErr    error
}

func (s *sseCommentStripper) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !s.done {
		if err := s.prepare(); err != nil {
			return 0, err
		}
	}
	if len(s.buf) > 0 {
		n := copy(p, s.buf)
		s.buf = s.buf[n:]
		return n, nil
	}
	if s.terminalErr != nil {
		return 0, s.terminalErr
	}
	return s.rc.Read(p)
}

func (s *sseCommentStripper) prepare() error {
	for {
		if s.ctx != nil {
			if err := s.ctx.Err(); err != nil {
				return s.fail(err)
			}
		}
		// Drain comment lines and blank separator lines from the front of buf.
		needMore := false
		for len(s.buf) > 0 {
			b := s.buf[0]
			switch b {
			case ':':
				// Skip entire comment line.
				idx := bytes.IndexByte(s.buf, '\n')
				if idx < 0 {
					if len(s.buf) > maxLeadingSSECommentLineBytes {
						return s.fail(fmt.Errorf(
							"%w (%d bytes)",
							errLeadingSSECommentLineTooLong,
							maxLeadingSSECommentLineBytes,
						))
					}
					needMore = true
					break
				}
				s.buf = s.buf[idx+1:]
			case '\n', '\r':
				s.buf = s.buf[1:]
			default:
				s.done = true
				return nil
			}
			if needMore {
				break
			}
		}
		if needMore && s.terminalErr != nil {
			// EOF terminates the final SSE comment line even without a newline.
			// Other read errors are surfaced after discarding the comment bytes.
			s.buf = nil
			s.done = true
			return s.terminalErr
		}
		if !needMore && len(s.buf) > 0 {
			continue
		}
		if s.terminalErr != nil {
			s.done = true
			return s.terminalErr
		}

		tmp := make([]byte, 512)
		n, err := s.rc.Read(tmp)
		if n > 0 {
			s.buf = append(s.buf, tmp[:n]...)
		}
		if err != nil {
			s.terminalErr = err
		}
		if n == 0 && err == nil {
			return s.fail(io.ErrNoProgress)
		}
	}
}

func (s *sseCommentStripper) fail(err error) error {
	s.buf = nil
	s.done = true
	s.terminalErr = err
	_ = s.closeUnderlying()
	return err
}

func (s *sseCommentStripper) closeUnderlying() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.rc.Close()
	})
	return s.closeErr
}

func (s *sseCommentStripper) Close() error {
	return s.closeUnderlying()
}

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
