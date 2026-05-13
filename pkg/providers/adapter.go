package providers

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/pkg/ai"
)

// ProviderAdapter defines the provider adapter contract.
type ProviderAdapter interface {
	API() string
	Stream(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.StreamOptions) (AssistantEventStream, error)
	StreamSimple(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions) (AssistantEventStream, error)
}

// ModelLister is an optional interface providers can implement to list available models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ai.Model, error)
}

// ProviderGetter provides provider lookup.
type ProviderGetter interface {
	Get(api string) (ProviderAdapter, bool)
}

// StreamFunc streams an assistant response for the given model and context.
type StreamFunc func(ctx context.Context, model ai.Model, aiCtx ai.Context, opts ai.StreamOptions) (AssistantEventStream, error)

// AdapterStreamFunc wraps a ProviderAdapter's Stream method as a StreamFunc.
func AdapterStreamFunc(adapter ProviderAdapter) StreamFunc {
	return adapter.Stream
}

// ErrProviderNotFound indicates missing provider registration.
var ErrProviderNotFound = errors.New("provider not found")
