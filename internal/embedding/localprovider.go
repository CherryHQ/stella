package embedding

import (
	"context"
	"fmt"
)

// LocalModelID is the vector-space key for the built-in multilingual-e5-small
// model. It MUST match the model id the sidecar stamps on its embeddings; vectors
// are namespaced by this string in storage. e5-small is 384-dim, zero-padded to
// the fixed storage width by ToStorageVector.
const LocalModelID = "intfloat/multilingual-e5-small@384"

// LocalEmbedder is the sidecar-backed embedding backend the local provider wraps.
// The composition root satisfies it with an adapter over the ML sidecar client,
// keeping the embedding package free of a direct dependency on internal/ml.
type LocalEmbedder interface {
	// EmbedLocal returns one vector per text, in order. mode is the embedding
	// package's Mode (query vs document); the adapter maps it to the sidecar's
	// prefix. A non-nil error is treated as transient by the chain unless wrapped
	// with Terminal.
	EmbedLocal(ctx context.Context, mode Mode, texts []string) ([][]float32, error)
}

// localProvider serves embeddings from the in-process ML sidecar. It is the
// KindLocal arm of the chain: privacy-sensitive requests are routed here, and a
// local-only deployment uses it as the sole provider.
type localProvider struct {
	embedder LocalEmbedder
}

// NewLocalProvider wraps a LocalEmbedder as a Provider in the e5-small space.
func NewLocalProvider(embedder LocalEmbedder) Provider {
	return &localProvider{embedder: embedder}
}

func (p *localProvider) Name() string  { return "stella-ml/e5-small" }
func (p *localProvider) Kind() Kind    { return KindLocal }
func (p *localProvider) Model() string { return LocalModelID }

func (p *localProvider) Embed(ctx context.Context, req Request) (Result, error) {
	vecs, err := p.embedder.EmbedLocal(ctx, req.Mode, req.Texts)
	if err != nil {
		return Result{}, err
	}
	if len(vecs) != len(req.Texts) {
		// A count mismatch is a protocol/data fault, not something failover fixes.
		return Result{}, Terminal(fmt.Errorf("local embedder returned %d vectors for %d texts", len(vecs), len(req.Texts)))
	}
	return Result{
		Vectors:      vecs,
		Model:        LocalModelID,
		ProviderName: p.Name(),
	}, nil
}
