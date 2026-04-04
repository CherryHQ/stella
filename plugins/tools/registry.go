package plugintools

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/tools"
)

// Factory creates a new tool instance.
type Factory func() tools.Tool

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register registers a plugin tool factory by name.
// Typically called from init() in each plugin tool package.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = factory
}

// Names returns all registered plugin tool names in sorted order.
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

// BuildEnabled queries the store for enabled tool plugins and returns
// instances of all registered tools that are enabled.
func BuildEnabled(ctx context.Context, store config.Store) []tools.Tool {
	mu.RLock()
	defer mu.RUnlock()

	var result []tools.Tool
	for name, factory := range registry {
		p, err := store.GetPlugin(ctx, config.PluginID(config.PluginKindTool, name))
		if err != nil {
			slog.Debug("plugin tool not found in store", "name", name, "error", err)
			continue
		}
		if !p.Enabled {
			continue
		}
		result = append(result, factory())
	}
	return result
}
