package plugintools

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/tools"
)

// BuildContext carries per-session configuration for tool construction.
type BuildContext struct {
	WorkDir     string // working directory for tool execution
	UserDataDir string // per-user sandbox directory (empty = no sandbox)
	AnnaHome    string // anna home directory (e.g. ~/.anna)
	Workspace   string // agent workspace dir
}

// Factory creates a tool given build context.
type Factory func(BuildContext) (tools.Tool, error)

// Registration holds a factory and metadata.
type Registration struct {
	Factory  Factory
	Required bool // required tools are always built; optional tools check store
}

var (
	mu       sync.RWMutex
	registry = map[string]Registration{}
)

// Register registers a plugin tool by name.
// Typically called from init() in each plugin tool package.
func Register(name string, reg Registration) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = reg
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

// BuildCore builds all Required tools. Called per-session by the runner.
func BuildCore(bc BuildContext) []tools.Tool {
	mu.RLock()
	defer mu.RUnlock()
	var result []tools.Tool
	for name, reg := range registry {
		if !reg.Required {
			continue
		}
		t, err := reg.Factory(bc)
		if err != nil {
			slog.Warn("failed to build core tool", "name", name, "error", err)
			continue
		}
		if t != nil {
			result = append(result, t)
		}
	}
	return result
}

// BuildEnabled queries the store for enabled optional (non-required) tool
// plugins and returns instances of all that are enabled.
func BuildEnabled(ctx context.Context, store config.Store, bc BuildContext) []tools.Tool {
	mu.RLock()
	defer mu.RUnlock()

	var result []tools.Tool
	for name, reg := range registry {
		if reg.Required {
			continue // core tools built via BuildCore
		}
		p, err := store.GetPlugin(ctx, config.PluginID(config.PluginKindTool, name))
		if err != nil {
			slog.Debug("plugin tool not found in store", "name", name, "error", err)
			continue
		}
		if !p.Enabled {
			continue
		}
		t, err := reg.Factory(bc)
		if err != nil {
			slog.Warn("failed to build plugin tool", "name", name, "error", err)
			continue
		}
		if t != nil {
			result = append(result, t)
		}
	}
	return result
}
