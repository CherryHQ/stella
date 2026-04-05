package pluginhooks

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/vaayne/anna/internal/config"
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

// BuildEnabled queries the store for enabled hook plugins and returns
// instances. Callers (NewHookSet) handle priority sorting.
func BuildEnabled(ctx context.Context, store config.Store) []hooks.HookPlugin {
	mu.RLock()
	defer mu.RUnlock()

	var result []hooks.HookPlugin
	for name, factory := range registry {
		p, err := store.GetPlugin(ctx, config.PluginID(config.PluginKindHook, name))
		if err != nil {
			slog.Debug("plugin hook not found in store", "name", name, "error", err)
			continue
		}
		if !p.Enabled {
			continue
		}
		result = append(result, factory())
	}

	return result
}
