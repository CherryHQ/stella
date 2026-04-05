package providers

import (
	"context"
	"errors"
	"sync"

	"github.com/vaayne/anna/pkg/ai"
)

// ProviderAdapter defines the provider adapter contract.
type ProviderAdapter interface {
	API() string
	Stream(model ai.Model, ctx ai.Context, opts ai.StreamOptions) (AssistantEventStream, error)
	StreamSimple(model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions) (AssistantEventStream, error)
}

// ModelLister is an optional interface providers can implement to list available models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ai.Model, error)
}

// ProviderGetter provides provider lookup.
type ProviderGetter interface {
	Get(api string) (ProviderAdapter, bool)
}

// ErrProviderNotFound indicates missing provider registration.
var ErrProviderNotFound = errors.New("provider not found")

// Registry stores providers by API name.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]ProviderAdapter
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]ProviderAdapter)}
}

var (
	defaultRegistry *Registry
	defaultOnce     sync.Once
)

// DefaultRegistry returns the process-wide provider registry singleton.
func DefaultRegistry() *Registry {
	defaultOnce.Do(func() {
		defaultRegistry = NewRegistry()
	})
	return defaultRegistry
}

// Register stores a provider by API key.
func (r *Registry) Register(provider ProviderAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.API()] = provider
}

// Get resolves a provider by API key.
func (r *Registry) Get(api string) (ProviderAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[api]
	return provider, ok
}

// Stream dispatches request to the registered API provider.
func Stream(model ai.Model, ctx ai.Context, opts ai.StreamOptions, providers ProviderGetter) (AssistantEventStream, error) {
	provider, ok := providers.Get(model.API)
	if !ok {
		return nil, ErrProviderNotFound
	}
	return provider.Stream(model, ctx, opts)
}

// StreamSimple dispatches simplified streaming to provider.
func StreamSimple(model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions, providers ProviderGetter) (AssistantEventStream, error) {
	provider, ok := providers.Get(model.API)
	if !ok {
		return nil, ErrProviderNotFound
	}
	return provider.StreamSimple(model, ctx, opts)
}

// Complete consumes a full stream and assembles an assistant message.
func Complete(model ai.Model, ctx ai.Context, opts ai.CompleteOptions, providers ProviderGetter) (ai.AssistantMessage, error) {
	eventStream, err := Stream(model, ctx, opts.StreamOptions, providers)
	if err != nil {
		return ai.AssistantMessage{}, err
	}

	msg := ai.AssistantMessage{Content: make([]ai.ContentBlock, 0, 4)}
	var text string
	var thinking string
	toolCalls := map[string]ai.ToolCall{}

	for event := range eventStream.Events() {
		switch e := event.(type) {
		case ai.EventTextDelta:
			text += e.Text
		case ai.EventThinkingDelta:
			thinking += e.Thinking
		case ai.EventToolCallDelta:
			call := toolCalls[e.ID]
			call.ID = e.ID
			if e.Name != "" {
				call.Name = e.Name
			}
			if call.Arguments == nil {
				call.Arguments = map[string]any{}
			}
			if e.Arguments != "" {
				call.Arguments["raw"] = e.Arguments
			}
			toolCalls[e.ID] = call
		case ai.EventUsage:
			msg.Usage = e.Usage
		case ai.EventStop:
			msg.StopReason = e.Reason
		case ai.EventError:
			if e.Err != nil {
				msg.ErrorMessage = e.Err.Error()
				msg.StopReason = ai.StopReasonError
			}
		}
	}

	if waitErr := eventStream.Wait(); waitErr != nil {
		return msg, waitErr
	}

	if text != "" {
		msg.Content = append(msg.Content, ai.TextContent{Text: text})
	}
	if thinking != "" {
		msg.Content = append(msg.Content, ai.ThinkingContent{Thinking: thinking})
	}
	for _, call := range toolCalls {
		msg.Content = append(msg.Content, call)
	}

	return msg, nil
}

// AssistantEventStream provides ordered assistant events.
type AssistantEventStream interface {
	Events() <-chan ai.AssistantEvent
	Close() error
	Wait() error
}

// ChannelEventStream is a channel-backed AssistantEventStream.
type ChannelEventStream struct {
	events chan ai.AssistantEvent
	once   sync.Once
	errMu  sync.RWMutex
	err    error
}

// NewChannelEventStream returns a writable channel stream.
func NewChannelEventStream(buffer int) *ChannelEventStream {
	return &ChannelEventStream{events: make(chan ai.AssistantEvent, buffer)}
}

// Events returns read-only event channel.
func (s *ChannelEventStream) Events() <-chan ai.AssistantEvent {
	return s.events
}

// Emit sends one event to subscribers.
func (s *ChannelEventStream) Emit(event ai.AssistantEvent) {
	s.events <- event
}

// Finish closes stream and stores terminal error if any.
func (s *ChannelEventStream) Finish(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
	s.once.Do(func() {
		close(s.events)
	})
}

// Close closes the event stream.
func (s *ChannelEventStream) Close() error {
	s.once.Do(func() {
		close(s.events)
	})
	return nil
}

// Wait returns terminal stream error.
func (s *ChannelEventStream) Wait() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.err
}
