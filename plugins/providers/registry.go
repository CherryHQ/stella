package pluginproviders

import (
	"context"
	"sort"
	"sync"

	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/config"
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
type Factory func(cfg ProviderConfig) ai.ProviderAdapter

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

// Build creates a single provider by name. Returns false if not registered.
func Build(name string, cfg ProviderConfig) (ai.ProviderAdapter, bool) {
	mu.RLock()
	reg, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, false
	}
	return reg.Factory(cfg), true
}

// BuildAll builds all registered providers that have a config entry
// and returns a populated ai.Registry.
func BuildAll(cfgs map[string]ProviderConfig) *ai.Registry {
	mu.RLock()
	snapshot := make(map[string]Registration, len(registry))
	for name, reg := range registry {
		snapshot[name] = reg
	}
	mu.RUnlock()

	r := ai.NewRegistry()
	for name, reg := range snapshot {
		cfg, ok := cfgs[name]
		if !ok {
			continue
		}
		r.Register(reg.Factory(cfg))
	}
	return r
}

// BuildEnabled queries the store for enabled provider plugins, extracts
// credentials from each plugin's config, and builds only those providers.
func BuildEnabled(ctx context.Context, store config.Store) *ai.Registry {
	plugins, err := store.ListPluginsByKind(ctx, config.PluginKindProvider)
	if err != nil {
		return ai.NewRegistry()
	}

	mu.RLock()
	snapshot := make(map[string]Registration, len(registry))
	for name, reg := range registry {
		snapshot[name] = reg
	}
	mu.RUnlock()

	r := ai.NewRegistry()
	for _, p := range plugins {
		if !p.Enabled {
			continue
		}
		reg, ok := snapshot[p.Name]
		if !ok {
			continue
		}
		apiKey, _ := p.Config["api_key"].(string)
		baseURL, _ := p.Config["base_url"].(string)
		r.Register(reg.Factory(ProviderConfig{APIKey: apiKey, BaseURL: baseURL}))
	}
	return r
}
