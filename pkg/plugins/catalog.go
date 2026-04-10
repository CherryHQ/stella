package plugins

import (
	"fmt"
	"sort"
	"sync"
)

// Plugin is the ownership unit in the unified plugin host.
// A plugin may register multiple capabilities against the provided host.
type Plugin interface {
	Register(host Host)
}

// PluginFunc adapts a function to the Plugin interface.
type PluginFunc func(host Host)

// Register implements Plugin.
func (f PluginFunc) Register(host Host) {
	if f != nil {
		f(host)
	}
}

// Catalog stores plugins by plugin ID.
type Catalog struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// NewCatalog creates an empty plugin catalog.
func NewCatalog() *Catalog {
	return &Catalog{plugins: make(map[string]Plugin)}
}

// Register stores a plugin by canonical plugin ID.
// It panics for invalid registrations because plugin init-time registration is a programmer error.
func (c *Catalog) Register(id string, plugin Plugin) {
	if id == "" {
		panic("plugins: empty plugin id")
	}
	if plugin == nil {
		panic(fmt.Sprintf("plugins: nil plugin for %q", id))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.plugins[id]; exists {
		panic(fmt.Sprintf("plugins: duplicate plugin id %q", id))
	}
	c.plugins[id] = plugin
}

// Get resolves a plugin by ID.
func (c *Catalog) Get(id string) (Plugin, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	plugin, ok := c.plugins[id]
	return plugin, ok
}

// Names returns all registered plugin IDs in sorted order.
func (c *Catalog) Names() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.plugins))
	for id := range c.plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

var defaultCatalog = NewCatalog()

// Register stores a plugin in the process-wide default catalog.
func Register(id string, plugin Plugin) {
	defaultCatalog.Register(id, plugin)
}

// Get resolves a plugin from the process-wide default catalog.
func Get(id string) (Plugin, bool) {
	return defaultCatalog.Get(id)
}

// Names returns all plugin IDs from the process-wide default catalog in sorted order.
func Names() []string {
	return defaultCatalog.Names()
}
