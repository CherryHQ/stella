package sandbox

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

// defaultRegistry is the global default registry instance.
var defaultRegistry = DefaultRegistry()

// GlobalRegistry returns the global default registry.
// This provides convenient access to the standard set of backends.
func GlobalRegistry() *Registry {
	return defaultRegistry
}

// Factory creates sessions from policies.
// Each backend implementation provides a Factory to validate and create sessions.
type Factory interface {
	// CreateSession validates policy against backend capabilities
	// and returns a Session. Fails closed if policy unsupported.
	//
	// If policy.Backend is non-empty and doesn't match this factory's Name(),
	// CreateSession should return an error.
	CreateSession(ctx context.Context, policy Policy) (Session, error)

	// Supported returns nil when the backend can satisfy the policy.
	// It returns an explanatory error when the policy is unsupported
	// or requires explicit relaxed mode.
	//
	// The returned error should be *PolicyCompatibilityError when
	// relaxed mode would allow partial enforcement.
	Supported(policy Policy) error

	// Name returns the backend identifier (e.g., "boxsh", "local").
	Name() string

	// Available reports whether this backend can be used on the current platform.
	Available() bool
}

// Registry manages available backend factories and creates sessions.
type Registry struct {
	factories map[string]Factory
	mu        sync.RWMutex
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
	}
}

// DefaultRegistry returns a registry with all built-in factories registered.
// This includes "boxsh" (when available) and "local" (always available).
func DefaultRegistry() *Registry {
	r := NewRegistry()

	// Register local factory (always available)
	_ = r.Register(&localFactory{})

	// Register boxsh factory if platform supports it
	if PlatformSupportsBoxsh() {
		_ = r.Register(&boxshFactory{})
	}

	return r
}

// Register adds a factory to the registry.
// Returns error if a factory with the same name already exists.
func (r *Registry) Register(factory Factory) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := factory.Name()
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("sandbox: factory %q already registered", name)
	}

	r.factories[name] = factory
	return nil
}

// Unregister removes a factory from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.factories, name)
}

// Get returns the factory with the given name, or nil if not found.
func (r *Registry) Get(name string) Factory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.factories[name]
}

// List returns all registered factory names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// AvailableBackends returns names of factories available on this platform.
func (r *Registry) AvailableBackends() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.factories))
	for name, factory := range r.factories {
		if factory.Available() {
			names = append(names, name)
		}
	}
	return names
}

// CreateSession creates a session using a compatible backend.
//
// Policy resolution rules:
//  1. If policy.Backend is non-empty, use that specific backend.
//  2. Otherwise, auto-select the first compatible backend.
//  3. If no backend supports the policy, fail closed with PolicyCompatibilityError.
//  4. If policy.Relaxed is false and backend can only partially enforce, fail.
//  5. If policy.Relaxed is true, allow partial enforcement.
//
// This method is safe for concurrent use.
func (r *Registry) CreateSession(ctx context.Context, policy Policy) (Session, error) {
	// First validate policy structure
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("sandbox: invalid policy: %w", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Case 1: Specific backend requested
	if policy.Backend != "" {
		factory, exists := r.factories[policy.Backend]
		if !exists {
			return nil, &PolicyCompatibilityError{
				Backend:          policy.Backend,
				Policy:           policy,
				Reason:           "backend not registered",
				RelaxedWouldHelp: false,
			}
		}

		if !factory.Available() {
			return nil, &PolicyCompatibilityError{
				Backend:          policy.Backend,
				Policy:           policy,
				Reason:           "backend not available on this platform",
				RelaxedWouldHelp: false,
			}
		}

		// Check if backend supports the policy
		if err := factory.Supported(policy); err != nil {
			return nil, err
		}

		return factory.CreateSession(ctx, policy)
	}

	// Case 2: Auto-select compatible backend
	// Try boxsh first if available, then fall through to local
	for _, name := range []string{"boxsh", "local"} {
		factory, exists := r.factories[name]
		if !exists || !factory.Available() {
			continue
		}

		// Check support
		if err := factory.Supported(policy); err != nil {
			// If this backend can't support the policy, try the next one
			if IsPolicyCompatibilityError(err) && !policy.Relaxed {
				// Continue to next backend
				continue
			}
			return nil, err
		}

		return factory.CreateSession(ctx, policy)
	}

	// No compatible backend found
	return nil, &PolicyCompatibilityError{
		Backend:          "any",
		Policy:           policy,
		Reason:           "no registered backend can satisfy this policy",
		RelaxedWouldHelp: true,
	}
}

// CreateRelaxedSession creates a session with relaxed policy.
// This is a convenience helper for explicit opt-in scenarios.
func (r *Registry) CreateRelaxedSession(ctx context.Context, base Policy) (Session, error) {
	base.Relaxed = true
	return r.CreateSession(ctx, base)
}

// PlatformSupportsBoxsh reports whether the current platform supports boxsh.
// This is a default implementation; specific platforms may override.
func PlatformSupportsBoxsh() bool {
	return PlatformRequiresBoxsh()
}

// PlatformRequiresBoxsh reports whether the current platform requires boxsh
// for sandboxing (linux and darwin).
func PlatformRequiresBoxsh() bool {
	return RequiresBoxsh(runtime.GOOS)
}
