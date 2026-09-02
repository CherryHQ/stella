// Package embedding turns text into vectors for semantic search. It exposes a
// single capability — Embed — behind a provider chain so the rest of Stella never
// knows or cares whether a given vector came from a remote API or a local model.
//
// The chain is API-first: a remote provider serves the request when healthy and
// the chain falls back to the next provider (e.g. a local model) only on
// transient failures, never on caller/data errors. See Chain for the rules.
//
// Vector-space safety. Every embedding lives in exactly one vector space — the
// (model, dimension) pair its provider produces. Vectors from different spaces
// are not comparable even at the same storage width, so Result carries the space
// key (Result.Model) that produced it and callers MUST filter stored vectors by
// it (WHERE model = result.Model). That turns a cross-space query into "no rows"
// instead of "wrong rows": a fallback that lands in a different space than the
// stored corpus simply matches nothing rather than returning garbage.
package embedding

import (
	"context"
	"errors"
)

// Mode distinguishes embedding a search query from embedding stored content.
// Some models embed the two asymmetrically (a query prompt vs a document
// prompt); providers that don't care ignore it.
type Mode string

const (
	ModeQuery    Mode = "query"
	ModeDocument Mode = "document"
)

// Privacy gates whether a request may leave the machine. A sensitive request
// skips every remote (KindAPI) provider in the chain and is served only by a
// local one; if none is configured it fails rather than silently going remote.
type Privacy string

const (
	PrivacyNormal    Privacy = "normal"
	PrivacySensitive Privacy = "sensitive"
)

// Kind is where a provider runs: a remote API, or a local in-process model.
type Kind string

const (
	KindAPI   Kind = "api"
	KindLocal Kind = "local"
)

// Request is one batch embed call. All texts are embedded in the same mode and
// returned in input order.
type Request struct {
	Texts   []string
	Mode    Mode
	Privacy Privacy
}

// Result holds the vectors for a Request plus the provenance a caller needs to
// store and later query them safely.
type Result struct {
	// Vectors aligns 1:1 with Request.Texts, in order. Each has the producing
	// provider's native dimension; widening to the fixed storage width is a
	// separate step at the persistence boundary, not the provider's job.
	Vectors [][]float32
	// Model is the vector-space key: the value to write into the *_embedding
	// "model" column and to filter on at query time. Two vectors are comparable
	// iff their Model matches.
	Model string
	// ProviderName is the provider that actually served the request (for metrics
	// and debugging); FallbackUsed is true when it was not the chain's primary.
	ProviderName string
	FallbackUsed bool
}

// Provider is one embedding backend. Implementations are expected to be safe for
// concurrent use.
type Provider interface {
	// Name identifies the provider instance in logs and Result.ProviderName.
	Name() string
	// Kind reports whether the provider is remote or local, which the chain uses
	// to honor PrivacySensitive.
	Kind() Kind
	// Model is the vector-space key every vector from this provider belongs to.
	Model() string
	// Embed returns one vector per input text, in order. It must return a
	// Terminal error (see IsTerminal) for caller/data faults that retrying or
	// failing over cannot fix; any other error is treated as transient and lets
	// the chain fall back.
	Embed(ctx context.Context, req Request) (Result, error)
}

// ErrNoProvider means the chain had no eligible provider for the request — e.g.
// a sensitive request with no local provider, or an empty chain.
var ErrNoProvider = errors.New("embedding: no eligible provider")

// terminalError marks a fault that must not trigger fallback: a caller or data
// error (auth failure, malformed input, oversized text, dimension/space
// mismatch) that every provider would reject identically. Transient faults
// (timeouts, 5xx, 429, connection errors) are left unwrapped so the chain fails
// over. Defining the distinction here keeps providers from each re-deriving it.
type terminalError struct{ err error }

func (e *terminalError) Error() string { return e.err.Error() }
func (e *terminalError) Unwrap() error { return e.err }

// Terminal wraps err so the chain stops instead of falling back. Use it for
// caller/data faults; leave transient faults unwrapped.
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &terminalError{err: err}
}

// IsTerminal reports whether err (or anything it wraps) was marked Terminal.
func IsTerminal(err error) bool {
	var t *terminalError
	return errors.As(err, &t)
}
