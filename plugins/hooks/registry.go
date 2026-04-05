package pluginhooks

import (
	"log/slog"
	"sort"
	"sync"

	"github.com/vaayne/anna/pkg/hooks"
)

// Factory creates a new hook plugin instance.
type Factory func() hooks.HookPlugin

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register registers a hook plugin factory by name.
// Typically called from init() in each plugin hook package.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = factory
}

// Names returns all registered hook plugin names in sorted order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// BuildEnabled builds hook plugins that the caller considers enabled.
// The enabled callback returns true for plugins that should be instantiated —
// typically by consulting the config store.
// Callers (NewHookSet) handle priority sorting.
func BuildEnabled(enabled func(name string) bool) []hooks.HookPlugin {
	mu.RLock()
	factories := make(map[string]Factory, len(registry))
	for name, factory := range registry {
		factories[name] = factory
	}
	mu.RUnlock()

	var result []hooks.HookPlugin
	for name, factory := range factories {
		if !enabled(name) {
			slog.Debug("plugin hook not enabled", "name", name)
			continue
		}
		result = append(result, factory())
	}

	return result
}
