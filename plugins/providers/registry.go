package pluginproviders

import (
	"fmt"
	"sort"
	"sync"

	"github.com/vaayne/anna/pkg/providers"
)

// ProviderConfig holds credentials passed to the factory at build time.
type ProviderConfig struct {
	APIKey  string
	BaseURL string
}

// ProviderMeta holds display metadata for the admin UI.
type ProviderMeta struct {
	Name       string // e.g. "Anthropic"
	DefaultURL string // e.g. "https://api.anthropic.com"
}

// Factory creates a provider adapter from config.
type Factory func(cfg ProviderConfig) providers.ProviderAdapter

// Registration holds a factory plus its metadata.
type Registration struct {
	Factory Factory
	Meta    ProviderMeta
}

var (
	mu       sync.RWMutex
	registry = map[string]Registration{}
)

// Register registers a provider plugin factory by name.
// Typically called from init() in each provider package.
func Register(name string, meta ProviderMeta, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = Registration{Factory: factory, Meta: meta}
}

// Names returns all registered provider names in sorted order.
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

// Build creates a single provider adapter by name. Returns false if not registered.
func Build(name string, cfg ProviderConfig) (providers.ProviderAdapter, bool) {
	mu.RLock()
	reg, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, false
	}
	return reg.Factory(cfg), true
}

// BuildRegistry creates a single-provider providers.Registry for the given name and config.
// This is the standard way to set up a provider for engine use.
func BuildRegistry(name string, cfg ProviderConfig) (*providers.Registry, error) {
	adapter, ok := Build(name, cfg)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	reg := providers.NewRegistry()
	reg.Register(adapter)
	return reg, nil
}
