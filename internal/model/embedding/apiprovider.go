package embedding

import (
	"context"
	"errors"
	"fmt"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/CherryHQ/stella/pkg/httpclient"
)

// APIConfig configures an OpenAI-compatible embedding API provider.
type APIConfig struct {
	// Name labels the provider instance in logs (default "openai-embedding").
	Name string
	// Model is the embedding model id sent to the API (e.g. "text-embedding-3-small").
	Model string
	// Provider is the canonical provider row the model belongs to. It never goes
	// on the wire; it exists so two accounts serving the same model name cannot
	// collapse into one vector space (see SpaceKey).
	Provider string
	// Dim is the output dimension to request. When > 0 it is sent as the API
	// `dimensions` parameter (supported by text-embedding-3-*), pinning output to
	// the storage width so no padding is needed. Leave 0 for models that don't
	// support it (the model's native dimension is then used).
	Dim     int
	APIKey  string
	BaseURL string
}

// SpaceKey is the vector-space identity written to and filtered on the
// *_embedding.model column. It names the space by (provider, model, dim), so any
// change to those yields a NEW key: old rows become backfill candidates (model
// mismatch) and queries point at the new space, instead of silently comparing a
// re-dimensioned — or differently-hosted — query against the old vectors.
//
// The provider half is what stops the subtle one: "text-embedding-3-small" from
// two different accounts or endpoints is two different embeddings of the same
// name, and comparing across them returns confident nonsense. A 0 dim (the
// model's stable native width) omits the suffix.
func (c APIConfig) SpaceKey() string {
	key := c.Model
	if c.Provider != "" {
		key = c.Provider + "/" + c.Model
	}
	if c.Dim > 0 {
		return fmt.Sprintf("%s@%d", key, c.Dim)
	}
	return key
}

// apiProvider is a remote, OpenAI-compatible embedding provider.
type apiProvider struct {
	name   string
	model  string // model id sent to the API
	space  string // vector-space key reported via Model() (model id + dim)
	dim    int
	client openai.Client
}

// NewAPIProvider builds a remote embedding provider over the OpenAI Embeddings
// API. It works with any OpenAI-compatible endpoint via APIConfig.BaseURL.
func NewAPIProvider(cfg APIConfig) Provider {
	name := cfg.Name
	if name == "" {
		name = "openai-embedding"
	}
	opts := []option.RequestOption{option.WithHTTPClient(httpclient.StdHTTPClient())}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &apiProvider{name: name, model: cfg.Model, space: cfg.SpaceKey(), dim: cfg.Dim, client: openai.NewClient(opts...)}
}

func (p *apiProvider) Name() string  { return p.name }
func (p *apiProvider) Kind() Kind    { return KindAPI }
func (p *apiProvider) Model() string { return p.space }

func (p *apiProvider) Embed(ctx context.Context, req Request) (Result, error) {
	if len(req.Texts) == 0 {
		return Result{Vectors: nil}, nil
	}
	params := openai.EmbeddingNewParams{
		Model: p.model,
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: req.Texts},
	}
	if p.dim > 0 {
		params.Dimensions = openai.Int(int64(p.dim))
	}

	resp, err := p.client.Embeddings.New(ctx, params)
	if err != nil {
		return Result{}, classifyErr(err)
	}

	// The API returns one datum per input but does not strictly guarantee order,
	// so place each by its Index. A short or misindexed response is a contract
	// violation: surface it as Terminal rather than failing over, since a
	// different provider would only produce vectors in a *different* space.
	out := make([][]float32, len(req.Texts))
	for i := range resp.Data {
		e := &resp.Data[i]
		idx := int(e.Index)
		if idx < 0 || idx >= len(out) || out[idx] != nil {
			return Result{}, Terminal(fmt.Errorf("embedding: API returned out-of-range/duplicate index %d for %d inputs", idx, len(out)))
		}
		// A provider that ignores the dimensions param would return its native width,
		// putting differently-sized vectors in one space key and silently corrupting
		// similarity. Reject the mismatch up front rather than store it.
		if p.dim > 0 && len(e.Embedding) != p.dim {
			return Result{}, Terminal(fmt.Errorf("embedding: API returned %d-dim vector, requested %d", len(e.Embedding), p.dim))
		}
		vec := make([]float32, len(e.Embedding))
		for j, f := range e.Embedding {
			vec[j] = float32(f)
		}
		out[idx] = vec
	}
	for i := range out {
		if out[i] == nil {
			return Result{}, Terminal(fmt.Errorf("embedding: API returned %d vectors for %d inputs", len(resp.Data), len(out)))
		}
	}
	return Result{Vectors: out}, nil
}

// classifyErr maps an SDK error to the chain's fallback contract: caller/data
// faults (4xx except 408/429) become Terminal so the chain stops; everything
// else — 5xx, rate limits, request timeouts, and transport errors — stays
// unwrapped so the chain fails over to the next provider.
func classifyErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch sc := apiErr.StatusCode; {
		case sc == 408 || sc == 429 || sc >= 500:
			return err // transient: retry/failover may help
		case sc >= 400:
			return Terminal(err) // auth, bad request, not found, unprocessable
		}
	}
	// Network errors, context deadlines, malformed responses: transient.
	return err
}
