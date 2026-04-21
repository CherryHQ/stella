package sandbox

import (
	"context"
	"fmt"
	"sync"
)

// Factory creates sessions from policies.
type Factory interface {
	CreateSession(ctx context.Context, policy Policy) (Session, error)
	Supported(policy Policy) error
	Name() string
	Available() bool
}

// Registry manages available backend factories and creates sessions.
type Registry struct {
	factories map[string]Factory
	order     []string
	mu        sync.RWMutex
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
		order:     make([]string, 0),
	}
}

// Register adds a factory to the registry.
func (r *Registry) Register(factory Factory) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := factory.Name()
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("sandbox: factory %q already registered", name)
	}

	r.factories[name] = factory
	r.order = append(r.order, name)
	return nil
}

// Unregister removes a factory from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.factories, name)
	for i, registered := range r.order {
		if registered == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// Get returns the factory with the given name, or nil if not found.
func (r *Registry) Get(name string) Factory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.factories[name]
}

// List returns all registered factory names in registration order.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if _, ok := r.factories[name]; ok {
			names = append(names, name)
		}
	}
	return names
}

// AvailableBackends returns available factory names in registration order.
func (r *Registry) AvailableBackends() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.order))
	for _, name := range r.order {
		factory, ok := r.factories[name]
		if ok && factory.Available() {
			names = append(names, name)
		}
	}
	return names
}

// CreateSession creates a session using a compatible backend.
func (r *Registry) CreateSession(ctx context.Context, policy Policy) (Session, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("sandbox: invalid policy: %w", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if policy.Backend != "" {
		factory, exists := r.factories[policy.Backend]
		if !exists {
			LogUnsupportedBackend(policy, []string{policy.Backend}, "backend not registered")
			return nil, &PolicyCompatibilityError{Backend: policy.Backend, Policy: policy, Reason: "backend not registered"}
		}
		if !factory.Available() {
			LogUnsupportedBackend(policy, []string{policy.Backend}, "backend not available on this platform")
			return nil, &PolicyCompatibilityError{Backend: policy.Backend, Policy: policy, Reason: "backend not available on this platform"}
		}
		if err := factory.Supported(policy); err != nil {
			return nil, err
		}
		return factory.CreateSession(ctx, policy)
	}

	attempted := make([]string, 0, len(r.order))
	for _, name := range r.order {
		factory, exists := r.factories[name]
		if !exists || !factory.Available() {
			continue
		}
		attempted = append(attempted, name)
		if err := factory.Supported(policy); err != nil {
			if IsPolicyCompatibilityError(err) {
				continue
			}
			return nil, err
		}
		return factory.CreateSession(ctx, policy)
	}

	LogUnsupportedBackend(policy, attempted, "no registered backend can satisfy this policy")
	return nil, &PolicyCompatibilityError{
		Backend: "any",
		Policy:  policy,
		Reason:  "no registered backend can satisfy this policy",
	}
}
