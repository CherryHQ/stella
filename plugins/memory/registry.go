package pluginmemory

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"

	"github.com/vaayne/anna/pkg/memory"
)

// BuildContext is passed to the factory when constructing a provider.
// Not all fields are populated for every plugin — a plugin that manages
// its own storage does not need DB.
type BuildContext struct {
	// DB is the shared SQLite connection. Provided when the plugin
	// wants to share the application database (e.g. the LCM plugin).
	// May be nil for plugins that manage their own storage.
	DB *sql.DB

	// AnnaHome is the path to ~/.anna/
	AnnaHome string

	// Config holds the plugin-specific configuration from settings_plugins.config JSON.
	// The plugin interprets this map however it needs.
	Config map[string]any

	// SummarizerFn provides LLM access for plugins that need to generate
	// summaries (e.g. LCM compaction). Injected by the pool manager.
	// Plugins that do not compact may ignore this. If nil, the LCM plugin
	// falls back to the deterministic truncation fallback.
	SummarizerFn func(ctx context.Context, prompt string) (string, error)
}

// Factory creates a Provider from a BuildContext.
type Factory func(ctx context.Context, bc BuildContext) (memory.Provider, error)

// ProviderMeta is displayed in the admin UI plugin list.
type ProviderMeta struct {
	Name         string   // display name (e.g. "Lossless Context Management")
	Description  string   // one-line description
	Capabilities []string // declared capability names for UI display
}

// Registration bundles a factory and its metadata.
type Registration struct {
	Factory Factory
	Meta    ProviderMeta
}

var (
	mu       sync.RWMutex
	registry = map[string]Registration{}
)

// Register adds a memory plugin to the global registry.
// Called from init() in each plugin package.
func Register(name string, reg Registration) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = reg
}

// Build constructs a named provider from the registry.
// Returns an error if the name is not registered.
func Build(ctx context.Context, name string, bc BuildContext) (memory.Provider, error) {
	mu.RLock()
	reg, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown memory plugin %q", name)
	}
	return reg.Factory(ctx, bc)
}

// List returns all registered plugin names in sorted order.
func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Get returns a registered memory provider factory by name.
func Get(name string) (Registration, bool) {
	mu.RLock()
	defer mu.RUnlock()
	reg, ok := registry[name]
	return reg, ok
}

// Metas returns a copy of all registered provider metadata keyed by name.
func Metas() map[string]ProviderMeta {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]ProviderMeta, len(registry))
	for name, reg := range registry {
		out[name] = reg.Meta
	}
	return out
}
