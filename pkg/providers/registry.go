package providers

import (
	"context"
	"sync"

	"github.com/CherryHQ/stella/pkg/ai"
)

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
func Stream(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.StreamOptions, providers ProviderGetter) (AssistantEventStream, error) {
	provider, ok := providers.Get(model.API)
	if !ok {
		return nil, ErrProviderNotFound
	}
	return provider.Stream(goCtx, model, ctx, opts)
}

// StreamSimple dispatches simplified streaming to provider.
func StreamSimple(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions, providers ProviderGetter) (AssistantEventStream, error) {
	provider, ok := providers.Get(model.API)
	if !ok {
		return nil, ErrProviderNotFound
	}
	return provider.StreamSimple(goCtx, model, ctx, opts)
}

// Complete consumes a full stream and assembles an assistant message.
func Complete(goCtx context.Context, model ai.Model, ctx ai.Context, opts ai.CompleteOptions, providers ProviderGetter) (ai.AssistantMessage, error) {
	eventStream, err := Stream(goCtx, model, ctx, opts.StreamOptions, providers)
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
