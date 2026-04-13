package sandbox

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

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
// Returns error if a factory with the same name already exists.
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

// List returns all registered factory names.
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

// AvailableBackends returns names of factories available on this platform.
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
			logUnsupportedBackend(policy, []string{policy.Backend}, "backend not registered")
			return nil, &PolicyCompatibilityError{
				Backend:          policy.Backend,
				Policy:           policy,
				Reason:           "backend not registered",
				RelaxedWouldHelp: false,
			}
		}

		if !factory.Available() {
			logUnsupportedBackend(policy, []string{policy.Backend}, "backend not available on this platform")
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

	// Case 2: Auto-select the first compatible backend in registration order.
	attempted := make([]string, 0, len(r.order))
	for _, name := range r.order {
		factory, exists := r.factories[name]
		if !exists || !factory.Available() {
			continue
		}
		attempted = append(attempted, name)

		if err := factory.Supported(policy); err != nil {
			if IsPolicyCompatibilityError(err) && !policy.Relaxed {
				continue
			}
			return nil, err
		}

		return factory.CreateSession(ctx, policy)
	}

	// No compatible backend found
	logUnsupportedBackend(policy, attempted, "no registered backend can satisfy this policy")
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
	switch runtime.GOOS {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}
