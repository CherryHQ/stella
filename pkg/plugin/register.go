package plugin

import "sync"

var (
	mu        sync.Mutex
	factories []Factory
)

// Register is called from Go plugin init() functions to register a plugin factory.
// It is safe for concurrent use.
func Register(f Factory) {
	mu.Lock()
	factories = append(factories, f)
	mu.Unlock()
}

// Factories returns a copy of all registered factories.
func Factories() []Factory {
	mu.Lock()
	defer mu.Unlock()
	result := make([]Factory, len(factories))
	copy(result, factories)
	return result
}

// ResetFactories clears the factory registry. Only for testing.
func ResetFactories() {
	mu.Lock()
	factories = nil
	mu.Unlock()
}
